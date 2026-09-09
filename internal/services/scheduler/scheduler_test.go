package scheduler_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/services/scheduler"
	"github.com/camden-brown/garrison/internal/tasks"
)

var at = time.Date(2026, 9, 9, 3, 59, 0, 0, time.UTC)

type fleet struct {
	mu      sync.Mutex
	jobs    []scheduler.Job
	players int
	known   bool
	fired   []string
	notices []string
}

func (f *fleet) Players(string) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.players, f.known
}

func (f *fleet) Schedules() []scheduler.Job {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.jobs
}

func (f *fleet) Submit(_ context.Context, server string, kind tasks.Kind, trigger tasks.Trigger) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fired = append(f.fired, string(kind)+"@"+server+"/"+trigger.String())
}

func (f *fleet) Notify(_ context.Context, server, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notices = append(f.notices, server+": "+text)
}

func (f *fleet) firings() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.fired...)
}

func (f *fleet) said() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.notices, "\n")
}

func nightly(t *testing.T, policy scheduler.Policy) scheduler.Job {
	t.Helper()
	spec, err := scheduler.Parse("0 4 * * *") // 04:00 daily
	if err != nil {
		t.Fatal(err)
	}
	return scheduler.Job{Server: "zomboid-main", Kind: tasks.KindRestart, Spec: spec, Policy: policy}
}

// clock returns a controllable now.
func clock(start time.Time) (*scheduler.Scheduler, func(time.Duration), *fleet) {
	f := &fleet{known: true}
	now := start
	s := scheduler.New(f)
	s.Now = func() time.Time { return now }
	return s, func(d time.Duration) { now = now.Add(d) }, f
}

// Starting Garrison must not fire everything that was due while it was closed.
// Running a missed nightly restart the moment the dashboard opens at ten in
// the morning is worse than skipping it — nobody asked, and four people are on.
func TestStartupDoesNotFireMissedJobs(t *testing.T) {
	s, advance, f := clock(at)
	f.jobs = []scheduler.Job{nightly(t, scheduler.PolicyAlways)}

	s.Seed()
	advance(30 * time.Second)
	s.Check(context.Background())

	if got := f.firings(); len(got) != 0 {
		t.Errorf("fired %v on startup, want nothing", got)
	}
}

func TestFiresWhenDue(t *testing.T) {
	s, advance, f := clock(at)
	f.jobs = []scheduler.Job{nightly(t, scheduler.PolicyAlways)}
	s.Seed()

	// 03:59 -> 04:01, past the 04:00 slot.
	advance(2 * time.Minute)
	s.Check(context.Background())

	got := f.firings()
	if len(got) != 1 || !strings.HasPrefix(got[0], "restart@zomboid-main") {
		t.Fatalf("fired %v, want one restart", got)
	}
	if !strings.HasSuffix(got[0], "scheduled") {
		t.Errorf("trigger = %q, want scheduled — how it was started decides what an alert says", got[0])
	}
}

// Once fired, it must not fire again until the next occurrence.
func TestDoesNotFireTwiceForOneSlot(t *testing.T) {
	s, advance, f := clock(at)
	f.jobs = []scheduler.Job{nightly(t, scheduler.PolicyAlways)}
	s.Seed()

	advance(2 * time.Minute)
	s.Check(context.Background())
	for i := 0; i < 20; i++ {
		advance(time.Minute)
		s.Check(context.Background())
	}

	if got := f.firings(); len(got) != 1 {
		t.Errorf("fired %d times for one slot: %v", len(got), got)
	}
}

// The policy that makes automation survivable: defer while anyone is online.
func TestSkipIfOccupiedDefersAndSaysSo(t *testing.T) {
	s, advance, f := clock(at)
	f.jobs = []scheduler.Job{nightly(t, scheduler.PolicySkipIfOccupied)}
	f.players = 3
	s.Seed()

	advance(2 * time.Minute)
	s.Check(context.Background())

	if got := f.firings(); len(got) != 0 {
		t.Errorf("fired %v with players online", got)
	}
	if said := f.said(); !strings.Contains(said, "deferred") || !strings.Contains(said, "3 player") {
		t.Errorf("notices = %q, want it to say why", said)
	}
}

// And runs once they leave.
func TestDeferredJobRunsWhenEveryoneLeaves(t *testing.T) {
	s, advance, f := clock(at)
	f.jobs = []scheduler.Job{nightly(t, scheduler.PolicySkipIfOccupied)}
	f.players = 3
	s.Seed()

	advance(2 * time.Minute)
	s.Check(context.Background())

	f.mu.Lock()
	f.players = 0
	f.mu.Unlock()

	advance(scheduler.RetryEvery + time.Minute)
	s.Check(context.Background())

	if got := f.firings(); len(got) != 1 {
		t.Errorf("fired %v, want one once the server emptied", got)
	}
}

// A job that retries forever is one nobody notices has stopped working.
func TestDeferredJobGivesUpAndSaysSo(t *testing.T) {
	s, advance, f := clock(at)
	f.jobs = []scheduler.Job{nightly(t, scheduler.PolicySkipIfOccupied)}
	f.players = 2
	s.Seed()

	advance(2 * time.Minute)
	s.Check(context.Background())

	for i := 0; i < 10; i++ {
		advance(scheduler.RetryEvery)
		s.Check(context.Background())
	}

	if got := f.firings(); len(got) != 0 {
		t.Errorf("fired %v while still occupied", got)
	}
	if said := f.said(); !strings.Contains(said, "giving up") {
		t.Errorf("notices = %q, want it to say it gave up", said)
	}
}

// Not knowing who is connected is not the same as nobody being connected.
// Restarting a server whose roster Garrison cannot see is how a scheduled job
// boots people out during the hour the log stream was broken.
func TestUnknownRosterDefersRatherThanAssumingEmpty(t *testing.T) {
	s, advance, f := clock(at)
	f.jobs = []scheduler.Job{nightly(t, scheduler.PolicySkipIfOccupied)}
	f.known = false
	s.Seed()

	advance(2 * time.Minute)
	s.Check(context.Background())

	if got := f.firings(); len(got) != 0 {
		t.Errorf("fired %v without knowing who was online", got)
	}
	if said := f.said(); !strings.Contains(said, "cannot see") {
		t.Errorf("notices = %q, want it to say the roster is unknown", said)
	}
}

// A backup nobody notices should not be deferred for players.
func TestAlwaysPolicyIgnoresPlayers(t *testing.T) {
	s, advance, f := clock(at)
	job := nightly(t, scheduler.PolicyAlways)
	job.Kind = tasks.KindBackup
	f.jobs = []scheduler.Job{job}
	f.players = 5
	s.Seed()

	advance(2 * time.Minute)
	s.Check(context.Background())

	if got := f.firings(); len(got) != 1 {
		t.Errorf("fired %v, want the backup to run regardless", got)
	}
}

// A job added while running is seeded rather than fired, for the same reason
// startup is.
func TestAJobAddedWhileRunningDoesNotFireImmediately(t *testing.T) {
	s, advance, f := clock(at)
	s.Seed()

	advance(time.Hour)
	f.mu.Lock()
	f.jobs = []scheduler.Job{nightly(t, scheduler.PolicyAlways)}
	f.mu.Unlock()
	s.Check(context.Background())

	if got := f.firings(); len(got) != 0 {
		t.Errorf("a newly configured job fired immediately: %v", got)
	}
}

// A typo in a cron expression should be a message, not a job that never runs.
func TestBadCronIsReportedNotDropped(t *testing.T) {
	jobs, problems := scheduler.JobsFrom([]model.Instance{{
		Name: "zomboid-main",
		Schedules: []model.Schedule{
			{Kind: "restart", Cron: "0 4 * * *", Policy: "skip-if-occupied"},
			{Kind: "backup", Cron: "every night please"},
		},
	}})

	if len(jobs) != 1 {
		t.Errorf("got %d jobs, want the one that parsed", len(jobs))
	}
	if len(problems) != 1 {
		t.Fatalf("got %d problems, want 1", len(problems))
	}
	if !strings.Contains(problems[0].Error(), "backup") {
		t.Errorf("problem does not name the schedule: %v", problems[0])
	}
}

// Cron's corner cases are the interesting parts, which is why this uses a
// library rather than a hand-rolled parser.
func TestSpecParsing(t *testing.T) {
	for _, expr := range []string{"0 4 * * *", "*/15 * * * *", "0 2 * * 0", "@daily", "30 3 1 * *"} {
		if _, err := scheduler.Parse(expr); err != nil {
			t.Errorf("Parse(%q) error = %v", expr, err)
		}
	}
	for _, expr := range []string{"", "nonsense", "99 * * * *", "* * *"} {
		if _, err := scheduler.Parse(expr); err == nil {
			t.Errorf("Parse(%q) succeeded", expr)
		}
	}
}

// Matching the current minute misses a job entirely if the process was busy
// when it came round, which for a nightly backup means finding out a week
// later.
func TestDueCatchesAMissedMinute(t *testing.T) {
	spec, err := scheduler.Parse("0 4 * * *")
	if err != nil {
		t.Fatal(err)
	}

	last := time.Date(2026, 9, 9, 3, 0, 0, 0, time.UTC)
	busy := time.Date(2026, 9, 9, 4, 7, 0, 0, time.UTC) // woke up seven minutes late

	if !spec.Due(last, busy) {
		t.Error("a job whose minute was missed never becomes due")
	}
}
