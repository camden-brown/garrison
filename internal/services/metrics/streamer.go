package metrics

import (
	"context"
	"time"

	"github.com/camden-brown/garrison/internal/host"
)

// Observer receives samples. The store implements it.
//
// Declared here by the producer, like fleet.Observer, so this package never
// imports internal/core — see ADR 0007.
type Observer interface {
	StatsSampled(ctx context.Context, instance string, s host.Sample)
}

// Streamer keeps exactly one stats stream per running container.
//
// This is where DESIGN's "memory over weeks" risk actually lives: the failure
// mode is a stream that reconnects without the old goroutine exiting, which
// costs nothing visible on day one and a gigabyte on day nine. So every stream
// has one owner, one context, and one place it is cancelled, and the bookkeeping
// lives in a single goroutine rather than behind a mutex.
//
// That is also why there is no lock here. A map of live streams guarded by a
// mutex would be the obvious shape and would put concurrency control outside
// internal/core; instead Run owns the map, and Reconcile is a channel send.
type Streamer struct {
	Driver   host.Driver
	Observer Observer

	// Now is the clock, injectable so a test asserts on a timestamp rather
	// than racing one.
	Now func() time.Time

	reconcile chan []host.Container
	departed  chan string
}

// NewStreamer returns a streamer that is not yet running.
func NewStreamer(driver host.Driver, obs Observer) *Streamer {
	return &Streamer{
		Driver:   driver,
		Observer: obs,
		// Buffered by one: a poll that arrives while the previous is still
		// being applied should not block the poller, and the newer set is
		// the one worth having.
		reconcile: make(chan []host.Container, 1),
		departed:  make(chan string, 16),
	}
}

// Reconcile tells the streamer which containers exist now. It never blocks:
// if a reconcile is already queued, the newer set replaces it, because a
// complete picture supersedes an older complete picture.
func (s *Streamer) Reconcile(containers []host.Container) {
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

// Run owns every stream until ctx is cancelled, then shuts them all down and
// waits for them. It blocks, so the caller can wait for it and a leaked
// goroutine shows up as a hang rather than as a process outliving its own UI.
func (s *Streamer) Run(ctx context.Context) {
	active := map[string]context.CancelFunc{}
	live := 0

	stopAll := func() {
		for _, cancel := range active {
			cancel()
		}
	}

	for {
		select {
		case <-ctx.Done():
			stopAll()
			// Drain: every stream reports its own exit, so waiting for
			// that many reports is waiting for all of them to be gone.
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
			wanted := map[string]string{} // container id -> instance
			for _, c := range containers {
				if c.State.Live() {
					wanted[c.ID] = c.Instance
				}
			}

			// Stop streams for containers that are gone or no longer up.
			for id, cancel := range active {
				if _, keep := wanted[id]; !keep {
					cancel()
					delete(active, id)
				}
			}

			// Start streams for containers that do not have one.
			for id, instance := range wanted {
				if _, running := active[id]; running {
					continue
				}
				streamCtx, cancel := context.WithCancel(ctx)
				active[id] = cancel
				live++
				go s.stream(streamCtx, id, instance)
			}
		}
	}
}

// stream consumes one container's samples until the stream ends or its context
// is cancelled, then reports its own departure exactly once.
//
// It does not reconnect. A container that stops producing stats has usually
// stopped; the next poll will notice and Reconcile will start a fresh stream if
// it is somehow still up. Reconnecting from in here is how the goroutine count
// climbs — the retry loop outlives the thing it was retrying.
func (s *Streamer) stream(ctx context.Context, id, instance string) {
	defer func() {
		select {
		case s.departed <- id:
		case <-ctx.Done():
			// Run is shutting down and is draining; it counts departures,
			// so it must still receive this one.
			s.departed <- id
		}
	}()

	samples, err := s.Driver.Stats(ctx, id)
	if err != nil {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case sample, ok := <-samples:
			if !ok {
				return
			}
			if sample.At.IsZero() {
				sample.At = s.now()
			}
			s.Observer.StatsSampled(ctx, instance, sample)
		}
	}
}

func (s *Streamer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
