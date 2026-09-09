package core

import (
	"context"
	"sync"
	"time"
)

// Store holds the snapshot and is the only thing that writes it.
//
// The mutex below is the one mutex in Garrison. It guards a single pointer
// assignment and is uncontended: the writer goroutine holds it for the length
// of a struct copy, and readers only take it to read the current value. A
// sync.Mutex anywhere outside this package means concurrency has leaked
// upward — send a mutation instead.
type Store struct {
	mu     sync.RWMutex
	snap   Snapshot
	subs   []chan Snapshot
	closed bool

	muts         chan Mutation
	ctl          Control
	now          func() time.Time
	metricLabels map[string]string
}

// Options configure a store. Every field has a working default so a test can
// say core.New(core.Options{}).
type Options struct {
	// Control performs the side effects the store's actions ask for. Nil
	// means actions report that no runtime is attached, which is what a
	// snapshot-only test wants.
	Control Control

	// Now is the clock, injectable so reducer tests are not timing tests.
	Now func() time.Time

	// Buffer sizes the mutation channel. Producers block when it is full,
	// which is correct: a mutation is a fact, and dropping facts to keep up
	// would make the snapshot quietly wrong.
	Buffer int

	// MetricLabels names the fourth dashboard tile per game id. It is
	// passed in rather than looked up so the store never imports the game
	// registry — the same seam as Control.
	MetricLabels map[string]string
}

// New returns a store that is not yet running. Nothing is applied until Run is
// called; sends before then queue in the buffer.
func New(opts Options) *Store {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Buffer <= 0 {
		opts.Buffer = 256
	}
	return &Store{
		muts:         make(chan Mutation, opts.Buffer),
		ctl:          opts.Control,
		now:          opts.Now,
		metricLabels: opts.MetricLabels,
		snap:         Snapshot{At: opts.Now()},
	}
}

// Run is the single writer. It applies mutations in the order received and
// publishes a snapshot after each one, until ctx is cancelled.
func (s *Store) Run(ctx context.Context) {
	defer s.closeSubs()
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-s.muts:
			s.applyAndPublish(m)
		}
	}
}

func (s *Store) applyAndPublish(m Mutation) {
	s.mu.Lock()
	next := m.apply(s.snap)
	next.Seq = s.snap.Seq + 1
	s.snap = next
	subs := s.subs
	s.mu.Unlock()

	for _, ch := range subs {
		publish(ch, next)
	}
}

// publish delivers to a subscriber without ever blocking the writer.
//
// The channel holds one snapshot. A subscriber that has not read the previous
// one gets it replaced rather than queued: a snapshot is a complete picture,
// so the newest is the only one worth rendering, and a render loop that fell
// behind should catch up rather than replay history.
//
// Drain-then-send is only safe with a single publisher, which is why Subscribe
// seeds a new channel under the lock rather than calling this. Two goroutines
// racing here could leave the older of two snapshots in the buffer, and at
// idle — no further mutations — that stale frame would be the last thing
// drawn.
func publish(ch chan Snapshot, snap Snapshot) {
	select {
	case ch <- snap:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- snap:
	default:
	}
}

// Send queues a mutation. It blocks only if the buffer is full, and returns
// early if ctx is cancelled during shutdown.
func (s *Store) Send(ctx context.Context, m Mutation) {
	select {
	case s.muts <- m:
	case <-ctx.Done():
	}
}

// Snapshot returns the current state. Safe to call from any goroutine.
//
// The value will not change underneath you: reducers copy on write and never
// touch a snapshot they have already published. The slices inside are shared
// with every other holder for exactly that reason, so a caller must not write
// to them — read, render, discard.
func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snap
}

// Subscribe returns a channel that carries the latest snapshot after every
// applied mutation, starting with the current one. The channel is closed when
// Run returns.
//
// The seed value is sent under the lock, which is safe because the channel is
// new, buffered and empty, so the send cannot block. Doing it here rather than
// through publish keeps the writer goroutine the only thing that ever
// conflates — see the note on publish.
func (s *Store) Subscribe() <-chan Snapshot {
	ch := make(chan Snapshot, 1)

	s.mu.Lock()
	defer s.mu.Unlock()

	ch <- s.snap
	if s.closed {
		// Run has already returned, so nothing will ever publish again.
		// Closing now means a caller ranging over this channel finishes
		// instead of blocking forever on a store that has shut down.
		close(ch)
		return ch
	}
	s.subs = append(s.subs, ch)
	return ch
}

func (s *Store) closeSubs() {
	s.mu.Lock()
	subs := s.subs
	s.subs = nil
	s.closed = true
	s.mu.Unlock()

	for _, ch := range subs {
		close(ch)
	}
}
