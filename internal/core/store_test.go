package core

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tasks"
)

func testStore(t *testing.T, engine Tasks) (*Store, context.Context) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	s := New(Options{Now: func() time.Time { return at }})
	if engine != nil {
		s.AttachTasks(engine)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Run did not return after cancellation")
		}
	})

	return s, ctx
}

// waitFor drains snapshots until one satisfies cond, so a test asserts on a
// condition rather than on how many frames it took to get there.
func waitFor(t *testing.T, sub <-chan Snapshot, what string, cond func(Snapshot) bool) Snapshot {
	t.Helper()

	deadline := time.After(2 * time.Second)
	for {
		select {
		case snap, ok := <-sub:
			if !ok {
				t.Fatalf("subscription closed while waiting for %s", what)
			}
			if cond(snap) {
				return snap
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestStoreAppliesAndPublishes(t *testing.T) {
	s, ctx := testStore(t, nil)
	sub := s.Subscribe()

	s.FleetObserved(ctx, at, []host.Container{{Instance: "zomboid-main", State: model.StateRunning}})

	snap := waitFor(t, sub, "the fleet", func(s Snapshot) bool { return len(s.Servers) == 1 })
	if snap.Servers[0].Name != "zomboid-main" {
		t.Errorf("server = %q", snap.Servers[0].Name)
	}
	if snap.Seq == 0 {
		t.Error("Seq did not increment")
	}
}

// A subscriber gets the current state immediately, so the first frame is not
// blank while it waits for something to change.
func TestSubscribeSeedsTheCurrentSnapshot(t *testing.T) {
	s, ctx := testStore(t, nil)

	s.FleetObserved(ctx, at, []host.Container{{Instance: "a"}})
	waitFor(t, s.Subscribe(), "the seeded snapshot", func(s Snapshot) bool { return len(s.Servers) == 1 })
}

// The conflation contract: a slow renderer sees the newest snapshot, never a
// backlog, and the writer is never blocked by it.
func TestPublishConflatesRatherThanQueueing(t *testing.T) {
	s, ctx := testStore(t, nil)
	sub := s.Subscribe()

	for i := 0; i < 50; i++ {
		s.Send(ctx, NoticeRaised{At: at, Text: "tick"})
	}

	final := waitFor(t, sub, "the last notice", func(s Snapshot) bool { return len(s.Notices) == 50 })
	if final.Seq == 0 {
		t.Error("Seq did not advance")
	}

	select {
	case extra := <-sub:
		t.Fatalf("a stale snapshot was queued behind the newest: seq %d", extra.Seq)
	default:
	}
}

func TestSubscribeAfterShutdownReturnsAClosedChannel(t *testing.T) {
	s := New(Options{})
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()
	cancel()
	<-done

	sub := s.Subscribe()
	<-sub // the seed value
	select {
	case _, ok := <-sub:
		if ok {
			t.Error("got a value from a store that has shut down")
		}
	case <-time.After(time.Second):
		t.Error("subscribing after shutdown blocks forever instead of closing")
	}
}

func TestRunClosesSubscriptionsOnShutdown(t *testing.T) {
	s := New(Options{})
	ctx, cancel := context.WithCancel(context.Background())
	sub := s.Subscribe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()
	cancel()
	<-done

	for range sub { //nolint:revive // drain to the close
	}
}

// recordingEngine stands in for the task engine, so the store's refusals and
// submissions can be checked without running any steps.
type recordingEngine struct {
	mu        sync.Mutex
	submitted []*tasks.Task
	cancelled []string
	seen      chan struct{}
}

func newEngine() *recordingEngine {
	return &recordingEngine{seen: make(chan struct{}, 16)}
}

func (e *recordingEngine) ID(kind tasks.Kind) string { return string(kind) + "-1" }

func (e *recordingEngine) Submit(_ context.Context, t *tasks.Task) {
	e.mu.Lock()
	e.submitted = append(e.submitted, t)
	e.mu.Unlock()
	select {
	case e.seen <- struct{}{}:
	default:
	}
}

func (e *recordingEngine) Cancel(_ context.Context, id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cancelled = append(e.cancelled, id)
}

func (e *recordingEngine) kinds() []tasks.Kind {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]tasks.Kind, 0, len(e.submitted))
	for _, t := range e.submitted {
		out = append(out, t.Kind)
	}
	return out
}

func (e *recordingEngine) waitFor(t *testing.T, what string) *tasks.Task {
	t.Helper()
	select {
	case <-e.seen:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.submitted) == 0 {
		return nil
	}
	return e.submitted[len(e.submitted)-1]
}

// applyWriteStep runs an apply task's first step, which is the one that
// writes the configuration. The engine would run it; this stands in for the
// engine so the store's half can be tested without one.
func applyWriteStep(t *testing.T, task *tasks.Task) {
	t.Helper()
	if task == nil || len(task.Steps) == 0 {
		t.Fatal("the apply task has no steps")
	}
	sc := &tasks.StepCtx{
		Values: map[string]any{},
		Log:    func(string) {},
		Revise: func(time.Duration) {},
	}
	_ = task.Steps[0].Run(context.Background(), sc)
}

// An action is a task submission, not work the store does itself. Everything
// slower than a frame goes through the engine — including this.
func TestStartSubmitsATask(t *testing.T) {
	engine := newEngine()
	s, ctx := testStore(t, engine)
	sub := s.Subscribe()

	s.FleetObserved(ctx, at, []host.Container{{Instance: "a", State: model.StateStopped}})
	waitFor(t, sub, "the fleet", func(s Snapshot) bool { return len(s.Servers) == 1 })

	s.Start(ctx, "a")
	engine.waitFor(t, "the task")

	if got := engine.kinds(); len(got) != 1 || got[0] != tasks.KindStart {
		t.Errorf("submitted %v, want one start", got)
	}
}

// What a server is busy with comes from the tasks in flight against it, so the
// two cannot disagree — which they did when a task failed in a way that
// skipped whatever was supposed to clear the flag.
func TestBusyIsDerivedFromTheTask(t *testing.T) {
	s, ctx := testStore(t, newEngine())
	sub := s.Subscribe()

	s.FleetObserved(ctx, at, []host.Container{{Instance: "a", State: model.StateRunning}})
	waitFor(t, sub, "the fleet", func(s Snapshot) bool { return len(s.Servers) == 1 })

	s.TaskProgressed(ctx, tasks.Progress{
		ID: "stop-1", Server: "a", Kind: tasks.KindStop, State: tasks.StateRunning,
		Steps: []string{"stop"},
	})
	snap := waitFor(t, sub, "the server to look busy", func(s Snapshot) bool {
		srv, ok := s.Server("a")
		return ok && srv.Busy == OpStop
	})
	if srv, _ := snap.Server("a"); srv.Busy.Present() != "stopping" {
		t.Errorf("Busy reads as %q", srv.Busy.Present())
	}

	s.TaskProgressed(ctx, tasks.Progress{
		ID: "stop-1", Server: "a", Kind: tasks.KindStop, State: tasks.StateDone,
		Steps: []string{"stop"},
	})
	snap = waitFor(t, sub, "busy to clear", func(s Snapshot) bool {
		srv, ok := s.Server("a")
		return ok && srv.Busy == OpNone
	})

	// A completed stop is what lets a later non-zero exit read as a
	// shutdown rather than a crash.
	if srv, _ := snap.Server("a"); !srv.StopRequested {
		t.Error("a completed stop task did not record that Garrison asked for it")
	}
}

func TestFailedTaskBecomesANotice(t *testing.T) {
	s, ctx := testStore(t, newEngine())
	sub := s.Subscribe()

	s.TaskProgressed(ctx, tasks.Progress{
		ID: "t1", Server: "a", Kind: tasks.KindRestart, State: tasks.StateRolledBack,
		Err: "a: stop: port 16261 already allocated",
	})

	snap := waitFor(t, sub, "the failure notice", func(s Snapshot) bool { return len(s.Notices) > 0 })
	if snap.Notices[0].Text != "a: stop: port 16261 already allocated" {
		t.Errorf("notice = %q", snap.Notices[0].Text)
	}
}

// A cancelled task is not a failure: the operator asked for it and already
// knows, so it does not raise an alert.
func TestCancelledTaskIsNotAnAlert(t *testing.T) {
	s, ctx := testStore(t, newEngine())
	sub := s.Subscribe()

	s.TaskProgressed(ctx, tasks.Progress{ID: "t1", Server: "a", State: tasks.StateCanceled, Err: "cancelled"})
	snap := waitFor(t, sub, "the task", func(s Snapshot) bool { return len(s.Tasks) == 1 })

	if len(snap.Notices) != 0 {
		t.Errorf("a cancellation raised %d notices, want none", len(snap.Notices))
	}
}

// The answer arrives as a sentence rather than a task that fails two seconds
// later for a reason the operator could have been told immediately.
func TestOperationsThatCannotSucceedAreRefusedUpFront(t *testing.T) {
	tests := []struct {
		name    string
		state   model.State
		created bool
		op      func(*Store, context.Context)
		want    string
	}{
		{
			name: "starting a running server", state: model.StateRunning, created: true,
			op:   func(s *Store, ctx context.Context) { s.Start(ctx, "a") },
			want: "a: start: already running",
		},
		{
			name: "stopping a stopped server", state: model.StateStopped, created: true,
			op:   func(s *Store, ctx context.Context) { s.Stop(ctx, "a") },
			want: "a: stop: already stopped",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := newEngine()
			s, ctx := testStore(t, engine)
			sub := s.Subscribe()

			s.FleetObserved(ctx, at, []host.Container{{Instance: "a", State: tt.state}})
			waitFor(t, sub, "the fleet", func(s Snapshot) bool { return len(s.Servers) == 1 })

			tt.op(s, ctx)
			snap := waitFor(t, sub, "the refusal", func(s Snapshot) bool { return len(s.Notices) > 0 })

			if snap.Notices[0].Text != tt.want {
				t.Errorf("notice = %q, want %q", snap.Notices[0].Text, tt.want)
			}
			if len(engine.kinds()) != 0 {
				t.Error("a task was submitted for an operation that should have been refused")
			}
		})
	}
}

// Unknown means the state is stale, not that the server is gone. The honest
// response to "start it anyway" is to try.
func TestUnknownStateDoesNotBlockAnOperation(t *testing.T) {
	engine := newEngine()
	s, ctx := testStore(t, engine)
	sub := s.Subscribe()

	s.FleetObserved(ctx, at, []host.Container{{Instance: "a", State: model.StateUnknown}})
	waitFor(t, sub, "the fleet", func(s Snapshot) bool { return len(s.Servers) == 1 })

	s.Start(ctx, "a")
	engine.waitFor(t, "the task")
}

func TestOperationOnAnUnknownServerIsReported(t *testing.T) {
	s, ctx := testStore(t, newEngine())
	sub := s.Subscribe()

	s.Start(ctx, "nope")

	snap := waitFor(t, sub, "the notice", func(s Snapshot) bool { return len(s.Notices) > 0 })
	if snap.Notices[0].Text != "nope: start: no such server" {
		t.Errorf("notice = %q", snap.Notices[0].Text)
	}
}

// Applying gathers the draft onto the configured settings and hands the whole
// instance to the task, so the task never has to merge anything.
func TestApplySettingsSubmitsTheMergedInstance(t *testing.T) {
	engine := newEngine()
	saver := &countingSaver{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	s := New(Options{Now: func() time.Time { return at }, Saver: saver})
	s.AttachTasks(engine)
	go s.Run(ctx)
	sub := s.Subscribe()

	s.InstancesLoaded(ctx, []model.Instance{{
		Name: "a", Game: "valheim", Settings: map[string]any{"ServerName": "Old", "WorldName": "W"},
	}})
	s.FleetObserved(ctx, at, []host.Container{{Instance: "a", Game: "valheim", State: model.StateRunning}})
	waitFor(t, sub, "the fleet", func(s Snapshot) bool { return len(s.Servers) == 1 })

	s.EditSetting(ctx, "a", "ServerName", "New")
	waitFor(t, sub, "the draft", func(s Snapshot) bool {
		srv, ok := s.Server("a")
		return ok && srv.Pending() == 1
	})

	s.ApplySettings(ctx, "a", true)
	engine.waitFor(t, "the apply task")

	if got := engine.kinds(); len(got) != 1 || got[0] != tasks.KindApplyConfig {
		t.Fatalf("submitted %v, want one apply", got)
	}
}

// Applying nothing does nothing rather than queueing a task that changes
// nothing and reports success.
func TestApplyingACleanFormDoesNothing(t *testing.T) {
	engine := newEngine()
	s, ctx := testStore(t, engine)
	sub := s.Subscribe()

	s.FleetObserved(ctx, at, []host.Container{{Instance: "a", State: model.StateRunning}})
	waitFor(t, sub, "the fleet", func(s Snapshot) bool { return len(s.Servers) == 1 })

	s.ApplySettings(ctx, "a", false)
	if len(engine.kinds()) != 0 {
		t.Error("applying a clean form submitted a task")
	}
}

type countingSaver struct {
	mu      sync.Mutex
	n       int
	deleted []string
	// err makes the save fail, to prove a draft survives one.
	err error
}

func (c *countingSaver) Save(model.Instance) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return c.err
	}
	c.n++
	return nil
}

// Delete satisfies the Saver interface. Deleting is recorded rather than done,
// which is all a fake needs.
func (c *countingSaver) Delete(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deleted = append(c.deleted, name)
	return nil
}

// The bug as reported: apply, answer yes, and the form asks to apply again.
//
// stillPending drops draft entries the configuration has caught up with, and
// it compares against the instance in the snapshot — which nothing updated
// after an apply, so the change stayed pending forever. The task writes the
// file; this is the store finding out.
func TestApplyingClearsTheDraft(t *testing.T) {
	engine := newEngine()
	saver := &countingSaver{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	s := New(Options{Now: func() time.Time { return at }, Saver: saver})
	s.AttachTasks(engine)
	go s.Run(ctx)
	sub := s.Subscribe()

	s.InstancesLoaded(ctx, []model.Instance{{
		Name: "a", Game: "valheim", Settings: map[string]any{"ServerName": "Old"},
	}})
	s.FleetObserved(ctx, at, []host.Container{{Instance: "a", Game: "valheim", State: model.StateRunning}})
	waitFor(t, sub, "the fleet", func(s Snapshot) bool { return len(s.Servers) == 1 })

	s.EditSetting(ctx, "a", "ServerName", "New")
	waitFor(t, sub, "the draft", func(s Snapshot) bool {
		srv, ok := s.Server("a")
		return ok && srv.Pending() == 1
	})

	s.ApplySettings(ctx, "a", true)
	task := engine.waitFor(t, "the apply task")

	// Run the task's config write the way the engine would. The saver the
	// store handed over is the one that reports back.
	applyWriteStep(t, task)

	waitFor(t, sub, "the draft to clear", func(s Snapshot) bool {
		srv, ok := s.Server("a")
		return ok && srv.Pending() == 0
	})

	srv, _ := s.Snapshot().Server("a")
	if got, _ := srv.Setting("ServerName"); got != "New" {
		t.Errorf("ServerName = %v after applying, want New", got)
	}
}

// A save that failed changed nothing, so the draft has to survive it — the
// edits exist only in memory and clearing them would lose the work.
func TestAFailedApplyKeepsTheDraft(t *testing.T) {
	engine := newEngine()
	saver := &countingSaver{err: errors.New("disk full")}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	s := New(Options{Now: func() time.Time { return at }, Saver: saver})
	s.AttachTasks(engine)
	go s.Run(ctx)
	sub := s.Subscribe()

	s.InstancesLoaded(ctx, []model.Instance{{
		Name: "a", Game: "valheim", Settings: map[string]any{"ServerName": "Old"},
	}})
	s.FleetObserved(ctx, at, []host.Container{{Instance: "a", Game: "valheim", State: model.StateRunning}})
	waitFor(t, sub, "the fleet", func(s Snapshot) bool { return len(s.Servers) == 1 })

	s.EditSetting(ctx, "a", "ServerName", "New")
	waitFor(t, sub, "the draft", func(s Snapshot) bool {
		srv, ok := s.Server("a")
		return ok && srv.Pending() == 1
	})

	s.ApplySettings(ctx, "a", true)
	task := engine.waitFor(t, "the apply task")
	applyWriteStep(t, task) // fails

	// Give the store a moment to have not cleared it.
	time.Sleep(50 * time.Millisecond)
	srv, _ := s.Snapshot().Server("a")
	if srv.Pending() != 1 {
		t.Errorf("pending = %d after a failed save, want the draft kept", srv.Pending())
	}
}
