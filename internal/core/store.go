package core

import (
	"context"
	"github.com/camden-brown/garrison/internal/model"
	"sync"
	"time"

	"github.com/camden-brown/garrison/internal/tasks"
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

	// stopped is closed when Run returns, so a mutation sent from a task
	// goroutine after shutdown is dropped rather than blocking that
	// goroutine forever on a channel nobody is reading.
	stopped chan struct{}

	muts         chan Mutation
	tasks        Tasks
	saver        Saver
	archives     Archives
	keepBackups  int
	commander    Commander
	now          func() time.Time
	metricLabels map[string]string
}

// Options configure a store. Every field has a working default so a test can
// say core.New(core.Options{}).
type Options struct {
	// Tasks runs the work the store's actions ask for. Nil means actions
	// report that no engine is attached, which is what a snapshot-only test
	// wants.
	Tasks Tasks

	// Saver writes server configuration, for the apply task.
	Saver Saver

	// Archives supplies a backup store per server. Nil means backups
	// report that there is nowhere to write them.
	Archives Archives

	// KeepBackups is how many archives to keep per server. Zero keeps them
	// all, which is a choice somebody should make rather than a default
	// that quietly fills a disk — the wizard will propose a number.
	KeepBackups int

	// Commander runs console commands. Nil means the console says it has no
	// channel rather than swallowing what was typed.
	Commander Commander

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
		stopped:      make(chan struct{}),
		tasks:        opts.Tasks,
		saver:        opts.Saver,
		archives:     opts.Archives,
		keepBackups:  opts.KeepBackups,
		commander:    opts.Commander,
		now:          opts.Now,
		metricLabels: opts.MetricLabels,
		snap:         Snapshot{At: opts.Now()},
	}
}

// Archives resolves a server to its backup store.
type Archives interface {
	For(server string) tasks.Archiver
}

// AttachTasks gives the store its engine.
//
// It is set after construction because the engine needs the store as its
// observer and the store needs the engine to submit to — a knot that has to be
// tied somewhere, and tying it here keeps both constructors honest about what
// they require.
func (s *Store) AttachTasks(t Tasks) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks = t
}

// Run is the single writer. It applies mutations in the order received and
// publishes a snapshot after each one, until ctx is cancelled.
func (s *Store) Run(ctx context.Context) {
	defer s.closeSubs()
	defer close(s.stopped)
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

// engine reads the attached task engine under the lock, because AttachTasks
// runs during startup while a view may already be dispatching.
func (s *Store) engine() Tasks {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tasks
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

// AttachCommander wires the console command runner after construction.
//
// Same knot as AttachTasks: the runner needs the resolver, the resolver needs
// the store, and cmd is where that circle is closed.
func (s *Store) AttachCommander(c Commander) { s.commander = c }

// sendFromTask delivers a mutation raised by a task step, which runs on its
// own goroutine and has no context of the store's to wait on.
func (s *Store) sendFromTask(m Mutation) {
	select {
	case s.muts <- m:
	case <-s.stopped:
	}
}

// taskSaver is the Saver handed to a task, wrapped so the store learns what
// the configuration on disk now says.
//
// This is what clears the "unapplied changes" badge. stillPending drops draft
// entries the configuration has caught up with, and it compares against the
// instance in the snapshot — which nothing was updating after an apply, so an
// applied change stayed pending forever and confirming it asked again.
//
// Wrapping the Saver rather than reporting from the action means the rollback
// is covered too: writeConfigStep's compensation writes the previous instance
// back, and the store hears about that the same way and restores the draft.
func (s *Store) taskSaver() Saver {
	if s.saver == nil {
		return nil
	}
	return notifyingSaver{inner: s.saver, store: s}
}

type notifyingSaver struct {
	inner Saver
	store *Store
}

func (n notifyingSaver) Save(inst model.Instance) error {
	if err := n.inner.Save(inst); err != nil {
		return err
	}
	// Only on success. A save that failed changed nothing, and reporting it
	// would clear a draft whose edits are still only in memory.
	n.store.sendFromTask(InstanceAdded{At: n.store.now(), Instance: inst})
	return nil
}

func (n notifyingSaver) Delete(name string) error { return n.inner.Delete(name) }
