// Package scheduler runs tasks on a timetable.
//
// Deliberately the last thing built in M2: a task engine you cannot watch is
// not one you should automate, so the Tasks view came first. This only decides
// when to press a button somebody could already press.
//
// The two policies are the difference between an automation that is useful and
// one turned off after a week. A nightly restart that boots four people out
// mid-raid gets disabled the next morning — so "skip if occupied" defers while
// anyone is online and gives up at a stated hour rather than lurking, and
// "wait for empty" holds the slot with a hard cap so it cannot wait forever.
package scheduler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tasks"
)

// Policy decides what a due job does when people are playing.
type Policy string

const (
	// PolicyAlways runs regardless. Right for a backup, which nobody
	// notices, and wrong for a restart.
	PolicyAlways Policy = "always"

	// PolicySkipIfOccupied defers while anyone is online, retrying hourly
	// and giving up at GiveUpAfter with something said about it.
	PolicySkipIfOccupied Policy = "skip-if-occupied"

	// PolicyWaitForEmpty holds the slot until the server empties, to the
	// same cap.
	PolicyWaitForEmpty Policy = "wait-for-empty"
)

// A deferred job keeps trying for this long and then says it gave up. One that
// retries forever is one nobody notices has stopped working.
const (
	RetryEvery  = time.Hour
	GiveUpAfter = 6 * time.Hour
)

// Fleet is what the scheduler needs from the rest of the program. The store
// implements it, which keeps this package free of internal/core.
type Fleet interface {
	// Players is how many are connected, and whether that is known at all.
	Players(server string) (n int, known bool)
	Schedules() []Job
	Submit(ctx context.Context, server string, kind tasks.Kind, trigger tasks.Trigger)
	// Notify reports something worth saying: a job deferred, or given up on.
	Notify(ctx context.Context, server, text string)
}

// Job is one scheduled task.
type Job struct {
	Server string
	Kind   tasks.Kind
	Spec   Spec
	Policy Policy
	Drain  time.Duration
}

// Key identifies a job across polls. Jobs come from config and have no id of
// their own, so it is built from what makes one distinct.
func (j Job) Key() string { return j.Server + "/" + string(j.Kind) + "/" + j.Spec.String() }

// Scheduler fires jobs when they are due.
type Scheduler struct {
	Fleet Fleet

	// Tick is how often due jobs are looked for. A minute is the finest
	// resolution five-field cron can express, so checking more often only
	// costs wakeups.
	Tick time.Duration

	// Now is the clock, injectable so a test runs a week in a millisecond.
	Now func() time.Time

	// deferred records when a job first wanted to run, so giving up is
	// measured from then rather than from the most recent attempt.
	deferred map[string]time.Time
	lastRun  map[string]time.Time
	lastTry  map[string]time.Time
}

// New returns a scheduler that is not yet running.
func New(fleet Fleet) *Scheduler {
	return &Scheduler{
		Fleet:    fleet,
		Tick:     time.Minute,
		deferred: map[string]time.Time{},
		lastRun:  map[string]time.Time{},
		lastTry:  map[string]time.Time{},
	}
}

// Run fires jobs until ctx is cancelled. It blocks.
func (s *Scheduler) Run(ctx context.Context) {
	tick := s.Tick
	if tick <= 0 {
		tick = time.Minute
	}

	// Seed the clock so nothing is considered overdue for the whole period
	// since the epoch the first time round.
	s.seed()

	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Check(ctx)
		}
	}
}

// Seed marks every job as having just been considered, so starting Garrison
// does not immediately fire everything that was due while it was closed.
//
// Running a missed nightly restart the moment the dashboard opens at ten in
// the morning is worse than skipping it: the operator did not ask for it and
// four people are online.
func (s *Scheduler) Seed() { s.seed() }

func (s *Scheduler) seed() {
	now := s.now()
	for _, job := range s.Fleet.Schedules() {
		if _, known := s.lastRun[job.Key()]; !known {
			s.lastRun[job.Key()] = now
		}
	}
}

// Check fires whatever is due. Exported so a test drives it directly rather
// than waiting on a ticker.
func (s *Scheduler) Check(ctx context.Context) {
	now := s.now()

	for _, job := range s.Fleet.Schedules() {
		key := job.Key()

		if _, known := s.lastRun[key]; !known {
			// A job added while running is seeded rather than fired, for
			// the same reason startup is.
			s.lastRun[key] = now
			continue
		}

		waiting, isWaiting := s.deferred[key]
		if isWaiting {
			if now.Sub(waiting) >= GiveUpAfter {
				delete(s.deferred, key)
				s.lastRun[key] = now
				s.Fleet.Notify(ctx, job.Server, fmt.Sprintf(
					"%s was due at %q and deferred for %s because people were online — giving up until its next scheduled time",
					job.Kind, job.Spec, GiveUpAfter))
				continue
			}
			if now.Sub(s.lastTry[key]) < RetryEvery {
				continue
			}
		} else if !job.Spec.Due(s.lastRun[key], now) {
			continue
		}

		s.lastTry[key] = now

		if reason, ok := s.clear(job); !ok {
			if !isWaiting {
				s.deferred[key] = now
				s.Fleet.Notify(ctx, job.Server, fmt.Sprintf(
					"%s deferred: %s. Retrying hourly for up to %s.", job.Kind, reason, GiveUpAfter))
			}
			continue
		}

		delete(s.deferred, key)
		s.lastRun[key] = now
		s.Fleet.Submit(ctx, job.Server, job.Kind, tasks.TriggerScheduled)
	}
}

// clear reports whether a job may run, and why not when it may not.
func (s *Scheduler) clear(job Job) (string, bool) {
	if job.Policy == PolicyAlways || job.Policy == "" {
		return "", true
	}

	players, known := s.Fleet.Players(job.Server)
	if !known {
		// Not knowing is not the same as empty. Restarting a server whose
		// roster Garrison cannot see is how a scheduled job boots people out
		// during the one hour the log stream was broken.
		return "Garrison cannot see who is connected", false
	}
	if players == 0 {
		return "", true
	}
	return fmt.Sprintf("%d player(s) online", players), false
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// JobsFrom turns configured schedules into jobs, reporting the ones that will
// not parse rather than dropping them silently. A typo in a cron expression
// should be a message, not a job that never runs.
func JobsFrom(instances []model.Instance) ([]Job, []error) {
	var jobs []Job
	var problems []error

	for _, inst := range instances {
		for _, sch := range inst.Schedules {
			spec, err := Parse(sch.Cron)
			if err != nil {
				problems = append(problems, fmt.Errorf("%s: schedule %q: %w", inst.Name, sch.Kind, err))
				continue
			}
			jobs = append(jobs, Job{
				Server: inst.Name,
				Kind:   tasks.Kind(strings.ToLower(sch.Kind)),
				Spec:   spec,
				Policy: Policy(sch.Policy),
				Drain:  sch.Drain,
			})
		}
	}
	return jobs, problems
}
