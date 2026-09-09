package scheduler

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// Spec is a parsed five-field cron expression.
//
// robfig/cron rather than a hand-rolled parser: cron's corner cases are all
// the interesting parts — step values, ranges, day-of-week versus
// day-of-month — and getting one wrong means a nightly backup that silently
// runs weekly.
type Spec struct {
	raw   string
	sched cron.Schedule
}

// Parse reads a standard five-field cron expression.
func Parse(expr string) (Spec, error) {
	if expr == "" {
		return Spec{}, fmt.Errorf("no schedule given")
	}
	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return Spec{}, fmt.Errorf("%q is not a cron expression: %w", expr, err)
	}
	return Spec{raw: expr, sched: sched}, nil
}

// String is the expression as written, for a message somebody has to act on.
func (s Spec) String() string { return s.raw }

// Due reports whether the job should have fired between last and now.
//
// It asks whether the next occurrence after the last run has arrived, rather
// than whether the current minute matches. Matching the minute misses a job
// entirely if the process was asleep or busy when it came round, which for a
// nightly backup means finding out a week later.
func (s Spec) Due(last, now time.Time) bool {
	if s.sched == nil {
		return false
	}
	if last.IsZero() {
		// Never run. Do not fire immediately on startup — that would make
		// every restart of Garrison trigger every job — but do fire at the
		// next genuine occurrence.
		return false
	}
	return !s.sched.Next(last).After(now)
}

// Next is when the job will fire after the given time, for showing a schedule.
func (s Spec) Next(after time.Time) time.Time {
	if s.sched == nil {
		return time.Time{}
	}
	return s.sched.Next(after)
}

// Valid reports whether the spec parsed.
func (s Spec) Valid() bool { return s.sched != nil }
