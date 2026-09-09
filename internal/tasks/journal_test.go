package tasks_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/host/fake"
	"github.com/camden-brown/garrison/internal/tasks"
)

// memJournal is a Journal that keeps everything, so a test can assert on what
// was written and when.
type memJournal struct {
	mu      sync.Mutex
	written []tasks.Progress
	stale   []tasks.Progress
}

func (j *memJournal) Record(p tasks.Progress) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.written = append(j.written, p)
	return nil
}

func (j *memJournal) Interrupted() ([]tasks.Progress, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.stale, nil
}

func (j *memJournal) all() []tasks.Progress {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]tasks.Progress(nil), j.written...)
}

// The record is written before the step, not after. The question it has to
// answer is "what was running when the power went out", and an entry written
// afterwards cannot answer it.
func TestProgressIsJournalledBeforeEachStep(t *testing.T) {
	j := &memJournal{}
	rec := newRecorder()
	e, ctx := engine(t, rec, j)

	seen := make(chan int, 4)
	step := func(n int) tasks.Step {
		return tasks.Step{Name: "step", Run: func(context.Context, *tasks.StepCtx) error {
			// Whatever the journal holds now must already include this step.
			count := 0
			for _, p := range j.all() {
				if p.State == tasks.StateRunning && p.Cursor == n {
					count++
				}
			}
			seen <- count
			return nil
		}}
	}

	e.Submit(ctx, &tasks.Task{ID: "t1", Server: "a", Kind: tasks.KindRestart,
		Steps: []tasks.Step{step(0), step(1), step(2)}})

	for i := 0; i < 3; i++ {
		select {
		case n := <-seen:
			if n == 0 {
				t.Errorf("step %d ran before its record was written", i)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out")
		}
	}
}

// A task the previous process left running is not resumed. It died mid-step,
// leaving the world in a state nobody recorded, and continuing a half-finished
// volume operation is how a world gets lost.
func TestInterruptedTasksAreFailedNotResumed(t *testing.T) {
	j := &memJournal{stale: []tasks.Progress{{
		ID: "old", Server: "zomboid-main", Kind: tasks.KindUpdate,
		State: tasks.StateRunning, Cursor: 4,
		Steps: []string{"warn", "save", "stop", "snapshot", "update image", "recreate"},
	}}}
	rec := newRecorder()

	e := tasks.New(fake.New(), stubResolver{}, rec, j)
	e.Now = func() time.Time { return at }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	rec.waitFor(t, "the interrupted task to be reported", func() bool {
		_, ok := rec.final("old")
		return ok
	})

	p, _ := rec.final("old")
	if p.State != tasks.StateFailed {
		t.Errorf("state = %v, want failed — an interrupted task is never resumed", p.State)
	}
	// The message has to say where it stopped, because that is the only
	// thing telling the operator what to check.
	for _, want := range []string{"interrupted", "step 5 of 6", "update image", "zomboid-main"} {
		if !strings.Contains(p.Err, want) {
			t.Errorf("error = %q, want it to mention %q", p.Err, want)
		}
	}
}

// An engine with no journal still works; nothing survives a restart.
func TestNilJournalIsFine(t *testing.T) {
	rec := newRecorder()
	e, ctx := engine(t, rec, nil)

	e.Submit(ctx, &tasks.Task{ID: "t1", Server: "a", Kind: tasks.KindStart,
		Steps: []tasks.Step{{Name: "x", Run: func(context.Context, *tasks.StepCtx) error { return nil }}}})

	rec.waitFor(t, "the task to finish", func() bool { _, ok := rec.final("t1"); return ok })
}

func TestProgressFraction(t *testing.T) {
	p := tasks.Progress{Steps: []string{"a", "b", "c", "d"}, Cursor: 2, State: tasks.StateRunning}
	if got := p.Fraction(); got != 0.5 {
		t.Errorf("Fraction() = %v, want 0.5", got)
	}
	if got := (tasks.Progress{Steps: []string{"a"}, State: tasks.StateDone}).Fraction(); got != 1 {
		t.Errorf("a finished task = %v, want 1", got)
	}
	if got := (tasks.Progress{}).Fraction(); got != 0 {
		t.Errorf("an empty task = %v, want 0", got)
	}
}
