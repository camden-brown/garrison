package tasks_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/host/fake"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tasks"
)

var at = time.Date(2026, 9, 9, 21, 0, 0, 0, time.UTC)

// recorder collects progress. The mutex is here because the engine reports
// from its lane goroutines while the test asserts from another.
type recorder struct {
	mu      sync.Mutex
	seen    []tasks.Progress
	changed chan struct{}
}

func newRecorder() *recorder { return &recorder{changed: make(chan struct{}, 256)} }

func (r *recorder) TaskProgressed(_ context.Context, p tasks.Progress) {
	r.mu.Lock()
	r.seen = append(r.seen, p)
	r.mu.Unlock()
	select {
	case r.changed <- struct{}{}:
	default:
	}
}

func (r *recorder) all() []tasks.Progress {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]tasks.Progress(nil), r.seen...)
}

func (r *recorder) final(id string) (tasks.Progress, bool) {
	var out tasks.Progress
	var found bool
	for _, p := range r.all() {
		if p.ID == id && p.State.Done() {
			out, found = p, true
		}
	}
	return out, found
}

func (r *recorder) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if cond() {
			return
		}
		select {
		case <-r.changed:
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

type stubResolver struct{ err error }

func (s stubResolver) Instance(server string) (model.Instance, tasks.Game, error) {
	if s.err != nil {
		return model.Instance{}, nil, s.err
	}
	return model.Instance{Name: server, Game: "valheim"}, stubGame{}, nil
}

type stubGame struct{}

func (stubGame) Plan(model.Instance) (model.Plan, error) { return model.Plan{Image: "x"}, nil }

// script records what ran, in order, so a test can assert on the sequence
// rather than on side effects.
type script struct {
	mu  sync.Mutex
	ran []string
}

func (s *script) note(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ran = append(s.ran, name)
}

func (s *script) order() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ran...)
}

func (s *script) step(name string, err error) tasks.Step {
	return tasks.Step{
		Name: name,
		Run: func(context.Context, *tasks.StepCtx) error {
			s.note("run " + name)
			return err
		},
		Undo: func(context.Context, *tasks.StepCtx) error {
			s.note("undo " + name)
			return nil
		},
	}
}

func engine(t *testing.T, obs tasks.Observer, journal tasks.Journal) (*tasks.Engine, context.Context) {
	t.Helper()

	e := tasks.New(fake.New(), stubResolver{}, obs, journal)
	e.Now = func() time.Time { return at }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Run did not return after cancellation")
		}
	})
	return e, ctx
}

func TestStepsRunInOrder(t *testing.T) {
	sc := &script{}
	rec := newRecorder()
	e, ctx := engine(t, rec, nil)

	e.Submit(ctx, &tasks.Task{
		ID: "t1", Server: "a", Kind: tasks.KindRestart,
		Steps: []tasks.Step{sc.step("one", nil), sc.step("two", nil), sc.step("three", nil)},
	})

	rec.waitFor(t, "the task to finish", func() bool { _, ok := rec.final("t1"); return ok })

	want := []string{"run one", "run two", "run three"}
	if got := sc.order(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("ran %v, want %v", got, want)
	}
	if p, _ := rec.final("t1"); p.State != tasks.StateDone {
		t.Errorf("state = %v, want done", p.State)
	}
}

// The property compensation exists for: a failure part way through must leave
// the world as it was, not somewhere in between.
func TestFailingStepUndoesEverythingBeforeItInReverse(t *testing.T) {
	sc := &script{}
	rec := newRecorder()
	e, ctx := engine(t, rec, nil)

	e.Submit(ctx, &tasks.Task{
		ID: "t1", Server: "a", Kind: tasks.KindUpdate,
		Steps: []tasks.Step{
			sc.step("one", nil),
			sc.step("two", nil),
			sc.step("three", errors.New("disk full")),
			sc.step("four", nil),
		},
	})

	rec.waitFor(t, "the task to finish", func() bool { _, ok := rec.final("t1"); return ok })

	want := []string{"run one", "run two", "run three", "undo three", "undo two", "undo one"}
	if got := sc.order(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("ran %v,\nwant %v", got, want)
	}

	p, _ := rec.final("t1")
	if p.State != tasks.StateRolledBack {
		t.Errorf("state = %v, want rolled back", p.State)
	}
	if !strings.Contains(p.Err, "disk full") {
		t.Errorf("error = %q, want the cause", p.Err)
	}
	if !strings.Contains(p.Err, "a:") {
		t.Errorf("error = %q, want it to name the server", p.Err)
	}
}

// A step with no compensation is skipped rather than blocking the rollback of
// the ones that do have it.
func TestStepsWithoutUndoAreSkipped(t *testing.T) {
	sc := &script{}
	rec := newRecorder()
	e, ctx := engine(t, rec, nil)

	noUndo := tasks.Step{Name: "read-only", Run: func(context.Context, *tasks.StepCtx) error {
		sc.note("run read-only")
		return nil
	}}

	e.Submit(ctx, &tasks.Task{
		ID: "t1", Server: "a", Kind: tasks.KindBackup,
		Steps: []tasks.Step{sc.step("one", nil), noUndo, sc.step("three", errors.New("boom"))},
	})

	rec.waitFor(t, "the task to finish", func() bool { _, ok := rec.final("t1"); return ok })

	want := []string{"run one", "run read-only", "run three", "undo three", "undo one"}
	if got := sc.order(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("ran %v,\nwant %v", got, want)
	}
}

// A compensation that fails must not stop the others: stopping there leaves
// more undone, not less.
func TestAFailedCompensationDoesNotStopTheRest(t *testing.T) {
	sc := &script{}
	rec := newRecorder()
	e, ctx := engine(t, rec, nil)

	badUndo := tasks.Step{
		Name: "two",
		Run:  func(context.Context, *tasks.StepCtx) error { sc.note("run two"); return nil },
		Undo: func(context.Context, *tasks.StepCtx) error {
			sc.note("undo two")
			return errors.New("could not undo")
		},
	}

	e.Submit(ctx, &tasks.Task{
		ID: "t1", Server: "a", Kind: tasks.KindUpdate,
		Steps: []tasks.Step{sc.step("one", nil), badUndo, sc.step("three", errors.New("boom"))},
	})

	rec.waitFor(t, "the task to finish", func() bool { _, ok := rec.final("t1"); return ok })

	if got := sc.order(); got[len(got)-1] != "undo one" {
		t.Errorf("ran %v, want the rollback to continue past the failed undo", got)
	}
}

// One lane per server, serialised: two tasks must never touch the same data
// volume, because a backup running while an update rewrites the world
// directory produces a corrupt archive discovered when you need it.
func TestOneServerRunsOneTaskAtATime(t *testing.T) {
	rec := newRecorder()
	e, ctx := engine(t, rec, nil)

	var mu sync.Mutex
	concurrent, peak := 0, 0
	slow := func(name string) tasks.Step {
		return tasks.Step{Name: name, Run: func(context.Context, *tasks.StepCtx) error {
			mu.Lock()
			concurrent++
			if concurrent > peak {
				peak = concurrent
			}
			mu.Unlock()

			time.Sleep(20 * time.Millisecond)

			mu.Lock()
			concurrent--
			mu.Unlock()
			return nil
		}}
	}

	for i := 0; i < 4; i++ {
		e.Submit(ctx, &tasks.Task{
			ID: "t" + string(rune('1'+i)), Server: "a", Kind: tasks.KindBackup,
			Steps: []tasks.Step{slow("work")},
		})
	}

	rec.waitFor(t, "all four to finish", func() bool {
		n := 0
		for i := 0; i < 4; i++ {
			if _, ok := rec.final("t" + string(rune('1'+i))); ok {
				n++
			}
		}
		return n == 4
	})

	mu.Lock()
	defer mu.Unlock()
	if peak > 1 {
		t.Errorf("%d tasks ran at once on one server, want 1", peak)
	}
}

// Lanes run in parallel across servers, because two servers have nothing to do
// with each other.
func TestDifferentServersRunInParallel(t *testing.T) {
	rec := newRecorder()
	e, ctx := engine(t, rec, nil)

	started := make(chan string, 4)
	release := make(chan struct{})
	block := tasks.Step{Name: "block", Run: func(ctx context.Context, s *tasks.StepCtx) error {
		started <- s.Instance.Name
		<-release
		return nil
	}}

	e.Submit(ctx, &tasks.Task{ID: "a1", Server: "a", Kind: tasks.KindBackup, Steps: []tasks.Step{block}})
	e.Submit(ctx, &tasks.Task{ID: "b1", Server: "b", Kind: tasks.KindBackup, Steps: []tasks.Step{block}})

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case name := <-started:
			seen[name] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d of 2 lanes started; they are not parallel", len(seen))
		}
	}
	close(release)

	if !seen["a"] || !seen["b"] {
		t.Errorf("started %v, want both servers", seen)
	}
}

// A queued task says why it is waiting, naming what is ahead of it. "Waiting"
// on its own invites a second press of the same key.
func TestAQueuedTaskSaysWhatItIsWaitingFor(t *testing.T) {
	rec := newRecorder()
	e, ctx := engine(t, rec, nil)

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	block := tasks.Step{Name: "block", Run: func(context.Context, *tasks.StepCtx) error {
		<-release
		return nil
	}}

	e.Submit(ctx, &tasks.Task{ID: "first", Server: "a", Kind: tasks.KindUpdate, Steps: []tasks.Step{block}})
	e.Submit(ctx, &tasks.Task{ID: "second", Server: "a", Kind: tasks.KindBackup, Steps: []tasks.Step{block}})

	rec.waitFor(t, "the queued task to explain itself", func() bool {
		for _, p := range rec.all() {
			if p.ID == "second" && p.State == tasks.StateQueued && p.Waiting != "" {
				return true
			}
		}
		return false
	})

	for _, p := range rec.all() {
		if p.ID == "second" && p.Waiting != "" {
			if !strings.Contains(p.Waiting, "first") {
				t.Errorf("waiting reason = %q, want it to name the task ahead", p.Waiting)
			}
			return
		}
	}
}

// Cancelling compensates rather than simply stopping, or the world is left
// exactly where compensation exists to avoid.
func TestCancelRollsBack(t *testing.T) {
	sc := &script{}
	rec := newRecorder()
	e, ctx := engine(t, rec, nil)

	reached := make(chan struct{})
	var once sync.Once
	wait := tasks.Step{
		Name: "wait",
		Run: func(ctx context.Context, s *tasks.StepCtx) error {
			sc.note("run wait")
			once.Do(func() { close(reached) })
			<-ctx.Done()
			return ctx.Err()
		},
		Undo: func(context.Context, *tasks.StepCtx) error { sc.note("undo wait"); return nil },
	}

	e.Submit(ctx, &tasks.Task{
		ID: "t1", Server: "a", Kind: tasks.KindRestart,
		Steps: []tasks.Step{sc.step("one", nil), wait},
	})

	<-reached
	e.Cancel(ctx, "t1")
	rec.waitFor(t, "the task to finish", func() bool { _, ok := rec.final("t1"); return ok })

	p, _ := rec.final("t1")
	if p.State != tasks.StateCanceled {
		t.Errorf("state = %v, want cancelled — a cancellation is not a failure", p.State)
	}

	order := strings.Join(sc.order(), ",")
	if !strings.Contains(order, "undo wait") || !strings.Contains(order, "undo one") {
		t.Errorf("ran %v, want both compensations", sc.order())
	}
}

// A cancelled task's compensations must still run. They inherit the cancelled
// context if nobody thinks about it, and then do nothing at all.
func TestCompensationRunsDespiteACancelledContext(t *testing.T) {
	rec := newRecorder()
	e, ctx := engine(t, rec, nil)

	undone := make(chan struct{})
	reached := make(chan struct{})
	var once sync.Once

	step := tasks.Step{
		Name: "wait",
		Run: func(ctx context.Context, s *tasks.StepCtx) error {
			once.Do(func() { close(reached) })
			<-ctx.Done()
			return ctx.Err()
		},
		Undo: func(ctx context.Context, s *tasks.StepCtx) error {
			if ctx.Err() != nil {
				return ctx.Err() // would mean the compensation was skipped
			}
			close(undone)
			return nil
		},
	}

	e.Submit(ctx, &tasks.Task{ID: "t1", Server: "a", Kind: tasks.KindRestart, Steps: []tasks.Step{step}})
	<-reached
	e.Cancel(ctx, "t1")

	select {
	case <-undone:
	case <-time.After(2 * time.Second):
		t.Fatal("the compensation never ran with a live context")
	}
}

// A task queued behind another can be dropped outright: nothing has happened
// yet, so there is nothing to compensate.
func TestCancellingAQueuedTaskDropsIt(t *testing.T) {
	sc := &script{}
	rec := newRecorder()
	e, ctx := engine(t, rec, nil)

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	block := tasks.Step{Name: "block", Run: func(context.Context, *tasks.StepCtx) error {
		<-release
		return nil
	}}

	e.Submit(ctx, &tasks.Task{ID: "first", Server: "a", Kind: tasks.KindUpdate, Steps: []tasks.Step{block}})
	e.Submit(ctx, &tasks.Task{ID: "second", Server: "a", Kind: tasks.KindBackup, Steps: []tasks.Step{sc.step("never", nil)}})

	rec.waitFor(t, "second to be queued", func() bool {
		for _, p := range rec.all() {
			if p.ID == "second" && p.State == tasks.StateQueued {
				return true
			}
		}
		return false
	})

	e.Cancel(ctx, "second")
	rec.waitFor(t, "second to be cancelled", func() bool { _, ok := rec.final("second"); return ok })

	for _, entry := range sc.order() {
		if entry == "run never" {
			t.Error("a cancelled queued task ran anyway")
		}
	}
}

func TestUnresolvableServerFailsTheTask(t *testing.T) {
	rec := newRecorder()
	e := tasks.New(fake.New(), stubResolver{err: errors.New("no such server")}, rec, nil)
	e.Now = func() time.Time { return at }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	e.Submit(ctx, &tasks.Task{ID: "t1", Server: "ghost", Kind: tasks.KindStart,
		Steps: []tasks.Step{{Name: "x", Run: func(context.Context, *tasks.StepCtx) error { return nil }}}})

	rec.waitFor(t, "the task to fail", func() bool { _, ok := rec.final("t1"); return ok })
	if p, _ := rec.final("t1"); p.State != tasks.StateFailed {
		t.Errorf("state = %v, want failed", p.State)
	}
}

// Steps hand values forward — the snapshot a later step restores, the
// container id a create produced.
func TestStepsPassValuesForward(t *testing.T) {
	rec := newRecorder()
	e, ctx := engine(t, rec, nil)

	got := make(chan string, 1)
	e.Submit(ctx, &tasks.Task{
		ID: "t1", Server: "a", Kind: tasks.KindBackup,
		Steps: []tasks.Step{
			{Name: "snapshot", Run: func(_ context.Context, s *tasks.StepCtx) error {
				s.Set("snapshot", "2026-09-09T2102.tar.zst")
				return nil
			}},
			{Name: "verify", Run: func(_ context.Context, s *tasks.StepCtx) error {
				v, _ := s.String("snapshot")
				got <- v
				return nil
			}},
		},
	})

	select {
	case v := <-got:
		if v != "2026-09-09T2102.tar.zst" {
			t.Errorf("second step read %q", v)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the value never arrived")
	}
}

var _ host.Driver = (*fake.Driver)(nil)

// Drainer satisfies the Resolver interface. A drain against a fake resolver
// has no channel, which is the same degradation a game without one gets.
func (stubResolver) Drainer(string) tasks.Drainer { return nil }
func (stubResolver) Mods(string) tasks.ModSync    { return nil }
