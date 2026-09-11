package tasks

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/model"
)

// Observer receives task progress. The store implements it.
//
// Declared here by the producer, like fleet.Observer, so this package never
// imports internal/core — see ADR 0007.
type Observer interface {
	TaskProgressed(ctx context.Context, p Progress)
}

// Journal is the durable record.
//
// Progress is written before every step, not after, because the question it
// has to answer is "what was running when the power went out" and an entry
// written afterwards cannot answer it.
type Journal interface {
	Record(p Progress) error
	// Interrupted returns tasks a previous process left running.
	Interrupted() ([]Progress, error)
}

// Resolver supplies what a task needs to know about the server it acts on.
// The engine is handed one rather than reaching for config itself.
type Resolver interface {
	Instance(server string) (model.Instance, Game, error)

	// Drainer is what a drain needs, with the transport already bound, or
	// nil when this server has no way to warn its players.
	//
	// Pre-bound because this package must not import internal/games:
	// internal/store depends on tasks, and the dependency rule forbids
	// anything under store from reaching games. So cmd — which is allowed
	// to know both — asserts games.Drainable and adapts it to this. The
	// arch test is what found that, which is the whole reason it exists.
	Drainer(server string) Drainer

	// Mods installs a server's mods, or nil for a game whose server fetches
	// its own from ids in its config — and for one with no mods at all.
	// Pre-bound for the same reason as Drainer: this package cannot see
	// games.Installable, and cmd can.
	Mods(server string) ModSync
}

// ModSync is the narrow capability an apply uses to put mod files in place.
//
// It returns its own compensation rather than being undone from outside: what
// a sync displaced is known only to the thing that displaced it, and a step
// that changes a volume has to declare how to take it back.
type ModSync interface {
	Sync(ctx context.Context, inst model.Instance, log func(string)) (undo func(context.Context) error, err error)
}

// Drainer is the narrow capability a drain uses.
type Drainer interface {
	// Warn tells everyone connected that the server goes down in `in`.
	Warn(ctx context.Context, in time.Duration) error
	// Save flushes the world.
	Save(ctx context.Context) error
}

// Engine runs tasks, one at a time per server.
type Engine struct {
	driver   host.Driver
	resolve  Resolver
	observer Observer
	journal  Journal

	// Now is the clock, injectable so a test asserts on a timestamp rather
	// than racing one.
	Now func() time.Time

	submit chan *Task
	cancel chan string
	done   chan laneResult

	// mu guards nothing but the id counter, which is the one thing the
	// public surface touches from another goroutine. Everything else lives
	// in Run.
	mu     sync.Mutex
	nextID int
}

// New returns an engine that is not yet running.
func New(driver host.Driver, resolve Resolver, obs Observer, journal Journal) *Engine {
	if journal == nil {
		journal = nopJournal{}
	}
	return &Engine{
		driver:   driver,
		resolve:  resolve,
		observer: obs,
		journal:  journal,
		submit:   make(chan *Task, 32),
		cancel:   make(chan string, 8),
		done:     make(chan laneResult, 8),
	}
}

// ID mints a task identifier. Exported so a caller can hold onto one it is
// about to submit.
func (e *Engine) ID(kind Kind) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.nextID++
	return fmt.Sprintf("%s-%d", kind, e.nextID)
}

// Submit queues a task. It never blocks for long: the engine takes it and the
// lane decides when it runs.
func (e *Engine) Submit(ctx context.Context, t *Task) {
	select {
	case e.submit <- t:
	case <-ctx.Done():
	}
}

// Cancel asks a running or queued task to stop. A running task finishes its
// current step and then rolls back, because interrupting a step halfway is
// exactly the state compensation cannot reason about.
func (e *Engine) Cancel(ctx context.Context, id string) {
	select {
	case e.cancel <- id:
	case <-ctx.Done():
	}
}

type laneResult struct {
	server string
	p      Progress
}

// Run owns every lane until ctx is cancelled. It blocks.
//
// One goroutine owns all the bookkeeping — the queues, which lane is busy,
// which task is cancelled — so there is no lock over any of it. The lanes
// themselves run concurrently, one goroutine per server, because two servers
// have nothing to do with each other.
func (e *Engine) Run(ctx context.Context) {
	e.reportInterrupted(ctx)

	queues := map[string][]*Task{}
	running := map[string]*runner{}
	live := 0

	for {
		select {
		case <-ctx.Done():
			for _, r := range running {
				r.cancel()
			}
			for live > 0 {
				<-e.done
				live--
			}
			return

		case t := <-e.submit:
			queues[t.Server] = append(queues[t.Server], t)
			e.announceQueue(ctx, queues[t.Server], running[t.Server])
			if running[t.Server] == nil {
				if r := e.start(ctx, &queues, t.Server); r != nil {
					running[t.Server] = r
					live++
				}
			}

		case id := <-e.cancel:
			for server, r := range running {
				if r.id == id {
					r.cancel()
					continue
				}
				_ = server
			}
			// A queued task can be dropped outright: nothing has happened
			// yet, so there is nothing to compensate.
			for server, q := range queues {
				for i, t := range q {
					if t.ID != id {
						continue
					}
					queues[server] = append(q[:i], q[i+1:]...)
					e.report(ctx, Progress{
						ID: t.ID, Server: t.Server, Kind: t.Kind, Trigger: t.Trigger,
						State: StateCanceled, Steps: stepNames(t.Steps),
						Ended: e.now(), Err: "cancelled before it started",
					})
					break
				}
			}

		case res := <-e.done:
			live--
			delete(running, res.server)
			if r := e.start(ctx, &queues, res.server); r != nil {
				running[res.server] = r
				live++
			}
			e.announceQueue(ctx, queues[res.server], running[res.server])
		}
	}
}

// start takes the next task for a server, if there is one.
func (e *Engine) start(ctx context.Context, queues *map[string][]*Task, server string) *runner {
	q := (*queues)[server]
	if len(q) == 0 {
		delete(*queues, server)
		return nil
	}

	t := q[0]
	(*queues)[server] = q[1:]

	runCtx, cancel := context.WithCancel(ctx)
	r := &runner{id: t.ID, cancel: cancel}

	go func() {
		p := e.execute(runCtx, t)
		e.done <- laneResult{server: server, p: p}
	}()
	return r
}

type runner struct {
	id     string
	cancel context.CancelFunc
}

// announceQueue tells a waiting task why it is waiting, naming the task ahead
// of it. "Waiting" on its own invites a second press of the same key.
func (e *Engine) announceQueue(ctx context.Context, queue []*Task, current *runner) {
	for _, t := range queue {
		waiting := "waiting for the lane"
		if current != nil {
			waiting = "waiting — " + current.id + " is running"
		}
		e.report(ctx, Progress{
			ID: t.ID, Server: t.Server, Kind: t.Kind, Trigger: t.Trigger,
			State: StateQueued, Steps: stepNames(t.Steps), Est: estimates(t.Steps),
			Queued: e.now(), Waiting: waiting,
		})
	}
}

// execute runs one task's steps in order, compensating on the way out.
func (e *Engine) execute(ctx context.Context, t *Task) Progress {
	p := Progress{
		ID: t.ID, Server: t.Server, Kind: t.Kind, Trigger: t.Trigger,
		State: StateRunning, Steps: stepNames(t.Steps), Est: estimates(t.Steps),
		Started: e.now(),
	}

	inst, game, err := e.resolve.Instance(t.Server)
	if err != nil {
		p.State, p.Err, p.Ended = StateFailed, err.Error(), e.now()
		e.record(ctx, p)
		return p
	}

	sc := &StepCtx{
		Driver:   e.driver,
		Instance: inst,
		Game:     game,
		Drain:    e.resolve.Drainer(t.Server),
		Mods:     e.resolve.Mods(t.Server),
		Values:   map[string]any{},
	}
	sc.Log = func(msg string) { p.History = append(p.History, msg) }

	for i, step := range t.Steps {
		p.Cursor = i
		sc.Revise = func(d time.Duration) {
			if i < len(p.Est) {
				p.Est[i] = d
			}
			e.report(ctx, p)
		}

		// Written before the step, not after: the question this record has
		// to answer is "what was running when the power went out", and an
		// entry written afterwards cannot answer it.
		e.record(ctx, p)

		if err := ctx.Err(); err != nil {
			return e.rollback(ctx, p, sc, t, i, StateCanceled, "cancelled")
		}

		if err := step.Run(ctx, sc); err != nil {
			// A cancellation mid-step is a cancellation, not a failure. The
			// difference decides whether the operator sees an alert.
			state, reason := StateFailed, fmt.Sprintf("%s: %s: %s", t.Server, step.Name, err)
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				state, reason = StateCanceled, "cancelled during "+step.Name
			}
			return e.rollback(ctx, p, sc, t, i, state, reason)
		}
	}

	p.Cursor = len(t.Steps)
	p.State, p.Ended = StateDone, e.now()
	e.record(ctx, p)
	return p
}

// rollback runs the compensations for every completed step, newest first.
//
// It uses a fresh context. The usual reason to be here is that the original
// was cancelled, and compensations that inherit a cancelled context do not
// run — which would leave exactly the half-finished state they exist to
// prevent.
func (e *Engine) rollback(parent context.Context, p Progress, sc *StepCtx, t *Task, failed int, state State, reason string) Progress {
	p.Err = reason

	undoCtx, cancel := context.WithTimeout(context.WithoutCancel(parent), rollbackBudget)
	defer cancel()

	compensated := false
	for i := failed; i >= 0; i-- {
		step := t.Steps[i]
		if step.Undo == nil {
			continue
		}
		compensated = true
		sc.Say("undo: " + step.Name)
		if err := step.Undo(undoCtx, sc); err != nil {
			// Best effort. A failed compensation is reported and the rest
			// still run: stopping here would leave more undone, not less.
			sc.Say(fmt.Sprintf("undo %s failed: %s", step.Name, err))
		}
	}

	if compensated && state == StateFailed {
		state = StateRolledBack
	}
	p.State, p.Ended = state, e.now()
	e.record(undoCtx, p)
	return p
}

// rollbackBudget bounds compensation. A rollback that hangs is worse than one
// that gives up and says so, because the operator is waiting to be told what
// state their world is in.
const rollbackBudget = 5 * time.Minute

// reportInterrupted deals with tasks a previous process left running.
//
// It does not resume them. A task that died mid-step left the world in a state
// nobody recorded, and automatically continuing a half-finished volume
// operation is how a world gets lost. It is marked failed, named, and put in
// front of the operator to decide.
func (e *Engine) reportInterrupted(ctx context.Context) {
	stale, err := e.journal.Interrupted()
	if err != nil {
		return
	}

	for _, p := range stale {
		p.State = StateFailed
		p.Ended = e.now()
		p.Err = fmt.Sprintf("%s: interrupted at step %d of %d (%s) — Garrison stopped while it was running",
			p.Server, p.Cursor+1, len(p.Steps), p.StepName())
		e.record(ctx, p)
	}
}

func (e *Engine) record(ctx context.Context, p Progress) {
	_ = e.journal.Record(p)
	e.report(ctx, p)
}

func (e *Engine) report(ctx context.Context, p Progress) {
	if e.observer != nil {
		e.observer.TaskProgressed(ctx, p)
	}
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func stepNames(steps []Step) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.Name
	}
	return out
}

func estimates(steps []Step) []time.Duration {
	out := make([]time.Duration, len(steps))
	for i, s := range steps {
		out[i] = s.Est
	}
	return out
}

// nopJournal is what an engine with no durable store uses. Everything still
// works; nothing survives a restart, and reportInterrupted finds nothing.
type nopJournal struct{}

func (nopJournal) Record(Progress) error            { return nil }
func (nopJournal) Interrupted() ([]Progress, error) { return nil, nil }
