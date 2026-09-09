// Package tasks is the engine for everything slower than a frame.
//
// A task is a named sequence of steps with a durable record, a declared
// compensation, and a lane. Nothing in Garrison does slow work outside it, so
// there is exactly one place that knows how to show progress, cancel safely,
// and survive a crash — which is the only way those three ever get done
// properly.
//
// This package must not import internal/core or internal/tui: it is handed a
// driver and reports what happened, and the store decides what that means.
package tasks

import (
	"context"
	"time"

	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/model"
)

// Kind names what a task is for. It is a string rather than an integer
// because it is written to the journal and read back by a later version.
type Kind string

const (
	KindStart       Kind = "start"
	KindStop        Kind = "stop"
	KindRestart     Kind = "restart"
	KindUpdate      Kind = "update"
	KindBackup      Kind = "backup"
	KindRestore     Kind = "restore"
	KindModSync     Kind = "modsync"
	KindApplyConfig Kind = "apply-config"
	KindCreate      Kind = "create"
)

// State is where a task got to.
type State uint8

const (
	StateQueued State = iota
	StateRunning
	StatePaused
	StateDone
	StateFailed
	StateCanceled
	// StateRolledBack means it failed and its compensations ran. The
	// distinction from Failed matters: one leaves the world as it was, the
	// other leaves it somewhere in between.
	StateRolledBack
)

var stateNames = [...]string{
	"queued", "running", "paused", "done", "failed", "canceled", "rolled back",
}

func (s State) String() string {
	if int(s) < len(stateNames) {
		return stateNames[s]
	}
	return "unknown"
}

// Done reports whether the task has stopped moving.
func (s State) Done() bool {
	return s == StateDone || s == StateFailed || s == StateCanceled || s == StateRolledBack
}

// Trigger records who asked.
type Trigger uint8

const (
	TriggerManual Trigger = iota
	TriggerScheduled
	TriggerChained
)

var triggerNames = [...]string{"manual", "scheduled", "chained"}

func (t Trigger) String() string {
	if int(t) < len(triggerNames) {
		return triggerNames[t]
	}
	return "manual"
}

// Step is one unit of work and the way to take it back.
type Step struct {
	Name string
	Run  func(ctx context.Context, s *StepCtx) error

	// Undo compensates this step. It runs best-effort in reverse order when
	// a later step fails or the task is cancelled, so it must tolerate
	// being called after a partial Run and must not fail the rollback.
	//
	// A step that changes a volume with no Undo is an undeclared risk. If
	// the compensation genuinely belongs to a neighbour — the way a restore
	// undoes everything after a snapshot — say so in a comment rather than
	// leaving the field silently nil.
	Undo func(ctx context.Context, s *StepCtx) error

	// Est is roughly how long it takes, for the progress bar. A wrong
	// estimate is a cosmetic problem; a missing one makes a long step look
	// like a hung one.
	Est time.Duration
}

// StepCtx is what a step is given: the world it acts on, and a place to leave
// notes for later steps.
type StepCtx struct {
	Driver   host.Driver
	Instance model.Instance
	Game     Game

	// Values carries state between steps of one task — the snapshot a later
	// step restores, the container id a create produced. It is a map rather
	// than fields because the set differs per kind, and a struct with every
	// kind's fields in it is a struct nobody can read.
	Values map[string]any

	// Log records what a step did, for the task's own history. It is not
	// the container's log.
	Log func(string)
}

// Set records a value for a later step.
func (s *StepCtx) Set(key string, v any) {
	if s.Values == nil {
		s.Values = map[string]any{}
	}
	s.Values[key] = v
}

// String reads a value a previous step left.
func (s *StepCtx) String(key string) (string, bool) {
	v, ok := s.Values[key].(string)
	return v, ok
}

// Say records a line in the task's history, tolerating a nil Log so a step is
// testable without one.
func (s *StepCtx) Say(msg string) {
	if s.Log != nil {
		s.Log(msg)
	}
}

// Game is the slice of the plugin surface a task needs.
//
// Declared here rather than taking games.Game so a task can be tested with a
// two-method stub, and so this package does not depend on the registry.
type Game interface {
	Plan(inst model.Instance) (model.Plan, error)
}

// Task is a sequence of steps against one server.
type Task struct {
	ID      string
	Server  string
	Kind    Kind
	Trigger Trigger
	Steps   []Step
}

// Progress is a task's state at a moment: what the engine reports and what the
// journal stores. It carries no functions, so it can be written to disk and
// read back by a later version of Garrison.
type Progress struct {
	ID      string
	Server  string
	Kind    Kind
	Trigger Trigger
	State   State

	// Cursor is the step being run, zero-based. Steps names the whole
	// sequence, so a view can draw the ones still to come.
	Cursor int
	Steps  []string
	Est    []time.Duration

	Queued  time.Time
	Started time.Time
	Ended   time.Time

	// Err is why it failed, already wrapped with the instance and the
	// operation so an alert can be shown verbatim.
	Err string

	// Waiting is why a queued task has not started: "update is on step 5/9".
	// Empty once it is running.
	Waiting string

	// History is what the steps said as they ran.
	History []string
}

// StepName is the current step's name, or empty when there is none.
func (p Progress) StepName() string {
	if p.Cursor < 0 || p.Cursor >= len(p.Steps) {
		return ""
	}
	return p.Steps[p.Cursor]
}

// Fraction is how far through the task is, for a progress bar.
func (p Progress) Fraction() float64 {
	if len(p.Steps) == 0 {
		return 0
	}
	if p.State == StateDone {
		return 1
	}
	return float64(p.Cursor) / float64(len(p.Steps))
}
