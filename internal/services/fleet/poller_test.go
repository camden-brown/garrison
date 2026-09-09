package fleet_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/host/fake"
	"github.com/camden-brown/garrison/internal/services/fleet"
)

var at = time.Date(2026, 9, 9, 21, 7, 0, 0, time.UTC)

// recorder is an Observer that a test can wait on. The mutex is here for the
// same reason the fake driver has one: the poller calls from its own
// goroutine while the test asserts from another.
type recorder struct {
	mu        sync.Mutex
	observed  [][]host.Container
	failures  []error
	described []host.Info
	changed   chan struct{}
}

func newRecorder() *recorder {
	return &recorder{changed: make(chan struct{}, 64)}
}

func (r *recorder) FleetObserved(ctx context.Context, ts time.Time, cs []host.Container) {
	r.mu.Lock()
	r.observed = append(r.observed, cs)
	r.mu.Unlock()
	r.ping()
}

func (r *recorder) FleetUnobservable(ctx context.Context, ts time.Time, err error) {
	r.mu.Lock()
	r.failures = append(r.failures, err)
	r.mu.Unlock()
	r.ping()
}

func (r *recorder) HostDescribed(ctx context.Context, ts time.Time, info host.Info) {
	r.mu.Lock()
	r.described = append(r.described, info)
	r.mu.Unlock()
	r.ping()
}

func (r *recorder) hostCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.described)
}

func (r *recorder) ping() {
	select {
	case r.changed <- struct{}{}:
	default:
	}
}

func (r *recorder) counts() (observed, failed int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.observed), len(r.failures)
}

func (r *recorder) waitFor(t *testing.T, what string, cond func(observed, failed int) bool) {
	t.Helper()

	deadline := time.After(2 * time.Second)
	for {
		if cond(r.counts()) {
			return
		}
		select {
		case <-r.changed:
		case <-deadline:
			o, f := r.counts()
			t.Fatalf("timed out waiting for %s (observed %d, failed %d)", what, o, f)
		}
	}
}

func run(t *testing.T, p *fleet.Poller, obs fleet.Observer) context.CancelFunc {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx, obs)
	}()

	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Run did not return after cancellation")
		}
	})
	return cancel
}

// The first frame must not be empty for a whole interval — that is the
// difference between launching into a fleet and launching into a blank table.
func TestPollerPollsImmediately(t *testing.T) {
	d := fake.New(fake.Running("zomboid-main", "zomboid", at))
	rec := newRecorder()

	run(t, &fleet.Poller{Driver: d, Interval: time.Hour, Now: func() time.Time { return at }}, rec)

	rec.waitFor(t, "the first poll", func(observed, _ int) bool { return observed >= 1 })
}

func TestPollerKeepsPolling(t *testing.T) {
	d := fake.New(fake.Running("a", "valheim", at))
	rec := newRecorder()

	run(t, &fleet.Poller{Driver: d, Interval: 5 * time.Millisecond}, rec)

	rec.waitFor(t, "three polls", func(observed, _ int) bool { return observed >= 3 })
}

func TestPollerReportsAnUnreachableEngine(t *testing.T) {
	d := fake.New(fake.Running("a", "valheim", at))
	d.SetDown(true)
	rec := newRecorder()

	run(t, &fleet.Poller{Driver: d, Interval: 5 * time.Millisecond}, rec)

	rec.waitFor(t, "a failure", func(_, failed int) bool { return failed >= 1 })
}

// The engine going away and coming back is the ordinary case — Docker Desktop
// restarts for an update — and the poller must recover on its own.
func TestPollerRecoversWhenTheEngineComesBack(t *testing.T) {
	d := fake.New(fake.Running("a", "valheim", at))
	d.SetDown(true)
	rec := newRecorder()

	run(t, &fleet.Poller{Driver: d, Interval: 5 * time.Millisecond}, rec)
	rec.waitFor(t, "a failure", func(_, failed int) bool { return failed >= 1 })

	d.SetDown(false)
	rec.waitFor(t, "a recovery", func(observed, _ int) bool { return observed >= 1 })
}

// Cancelling mid-call is Garrison's own doing. Reporting it would raise
// "docker unreachable" on the way out of a clean shutdown.
func TestPollerDoesNotReportItsOwnCancellation(t *testing.T) {
	d := fake.New()
	d.SetFail("List", errors.New("context canceled"))
	rec := newRecorder()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	p := &fleet.Poller{Driver: d, Interval: time.Hour}
	p.Run(ctx, rec)

	if observed, failed := rec.counts(); observed != 0 || failed != 0 {
		t.Errorf("reported (observed %d, failed %d) for a cancelled context, want nothing", observed, failed)
	}
}

func TestPollerDefaultsItsInterval(t *testing.T) {
	d := fake.New()
	rec := newRecorder()

	run(t, &fleet.Poller{Driver: d}, rec)

	rec.waitFor(t, "the first poll", func(observed, _ int) bool { return observed >= 1 })
}

// The host's figures are what turn usage into a proportion, so they are read
// once at startup rather than waited for.
func TestPollerDescribesTheHostImmediately(t *testing.T) {
	d := fake.New()
	d.SetInfo(host.Info{Version: "29.7.2", NCPU: 16, MemTotal: 64 << 30})
	rec := newRecorder()

	run(t, &fleet.Poller{Driver: d, Interval: time.Hour}, rec)

	rec.waitFor(t, "the host description", func(_, _ int) bool { return rec.hostCount() >= 1 })

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if got := rec.described[0].NCPU; got != 16 {
		t.Errorf("NCPU = %d, want 16", got)
	}
}

// An unreachable engine fails the fleet poll, which already says so once. A
// second failure report for the same outage is noise.
func TestPollerStaysQuietAboutHostWhenTheEngineIsDown(t *testing.T) {
	d := fake.New()
	d.SetDown(true)
	rec := newRecorder()

	run(t, &fleet.Poller{Driver: d, Interval: 5 * time.Millisecond}, rec)
	rec.waitFor(t, "a fleet failure", func(_, failed int) bool { return failed >= 1 })

	if got := rec.hostCount(); got != 0 {
		t.Errorf("described the host %d times while the engine was down, want 0", got)
	}
}
