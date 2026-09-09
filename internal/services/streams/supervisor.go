// Package streams runs one goroutine per live container and shuts them all
// down cleanly.
//
// It exists once because getting it wrong is expensive and getting it wrong
// twice is likely. DESIGN's "memory over weeks" risk is a stream that
// reconnects without the old goroutine exiting: invisible on day one, a
// gigabyte on day nine. Both the stats stream and the log stream need exactly
// this, so the accounting lives here and is tested here.
package streams

import (
	"context"

	"github.com/camden-brown/garrison/internal/host"
)

// Supervisor keeps one goroutine per running container.
//
// There is no mutex. A map of live streams behind one would be the obvious
// shape and would put concurrency control outside internal/core; instead Run
// owns the map and everything reaches it through channels.
type Supervisor struct {
	// Stream runs in its own goroutine for one container. It must return
	// when ctx is cancelled, and it must not retry on its own: a stream
	// that ends is reported, and the next Reconcile starts a fresh one if
	// the container is still up. A retry loop inside here outlives the
	// thing it was retrying, which is how the goroutine count climbs.
	stream func(ctx context.Context, c host.Container)

	reconcile chan []host.Container
	departed  chan string
}

// New returns a supervisor that is not yet running.
func New(stream func(ctx context.Context, c host.Container)) *Supervisor {
	return &Supervisor{
		stream: stream,
		// Buffered by one: a poll arriving while the previous is still
		// being applied must not block the poller, and the newer fleet
		// supersedes the older one anyway.
		reconcile: make(chan []host.Container, 1),
		departed:  make(chan string, 32),
	}
}

// Reconcile tells the supervisor which containers exist now. It never blocks:
// a queued fleet is replaced rather than queued behind, because a complete
// picture supersedes an older complete picture.
func (s *Supervisor) Reconcile(containers []host.Container) {
	select {
	case s.reconcile <- containers:
		return
	default:
	}
	select {
	case <-s.reconcile:
	default:
	}
	select {
	case s.reconcile <- containers:
	default:
	}
}

// Run owns every stream until ctx is cancelled, then stops them and waits.
//
// It blocks, so the caller can wait for it and a leaked stream shows up as a
// hang in a test rather than as a process quietly outliving its own UI.
func (s *Supervisor) Run(ctx context.Context) {
	active := map[string]context.CancelFunc{}
	live := 0

	for {
		select {
		case <-ctx.Done():
			for _, cancel := range active {
				cancel()
			}
			// Every stream reports its own exit exactly once, so waiting
			// for that many reports is waiting for all of them.
			for live > 0 {
				<-s.departed
				live--
			}
			return

		case id := <-s.departed:
			if cancel, ok := active[id]; ok {
				cancel()
				delete(active, id)
			}
			live--

		case containers := <-s.reconcile:
			wanted := map[string]host.Container{}
			for _, c := range containers {
				if c.State.Live() {
					wanted[c.ID] = c
				}
			}

			for id, cancel := range active {
				if _, keep := wanted[id]; !keep {
					cancel()
					delete(active, id)
				}
			}

			for id, c := range wanted {
				if _, running := active[id]; running {
					continue
				}
				streamCtx, cancel := context.WithCancel(ctx)
				active[id] = cancel
				live++
				go s.run(streamCtx, c)
			}
		}
	}
}

// run wraps one stream so its departure is reported exactly once, whatever
// the stream does — including panicking, which would otherwise leave Run
// waiting forever for a report that never comes.
func (s *Supervisor) run(ctx context.Context, c host.Container) {
	defer func() {
		select {
		case s.departed <- c.ID:
		case <-ctx.Done():
			// Run is draining and counts departures, so it must still
			// receive this one.
			s.departed <- c.ID
		}
	}()
	s.stream(ctx, c)
}
