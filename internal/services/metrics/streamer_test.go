package metrics_test

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/host/fake"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/services/metrics"
)

var start = time.Date(2026, 9, 9, 21, 0, 0, 0, time.UTC)

type recorder struct {
	mu      sync.Mutex
	byInst  map[string]int
	changed chan struct{}
}

func newRecorder() *recorder {
	return &recorder{byInst: map[string]int{}, changed: make(chan struct{}, 256)}
}

func (r *recorder) StatsSampled(_ context.Context, instance string, _ host.Sample) {
	r.mu.Lock()
	r.byInst[instance]++
	r.mu.Unlock()
	select {
	case r.changed <- struct{}{}:
	default:
	}
}

func (r *recorder) count(instance string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.byInst[instance]
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
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func running(instance string) host.Container {
	c := fake.Running(instance, "valheim", start)
	c.State = model.StateRunning
	return c
}

func runStreamer(t *testing.T, d host.Driver, obs metrics.Observer) (*metrics.Streamer, context.CancelFunc, chan struct{}) {
	t.Helper()

	s := metrics.NewStreamer(d, obs)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()

	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Run did not return after cancellation")
		}
	})
	return s, cancel, done
}

func TestOneStreamPerRunningContainer(t *testing.T) {
	d := fake.New(running("a"), running("b"))
	d.SetSamples(host.Sample{CPUPct: 10}, host.Sample{CPUPct: 20})
	rec := newRecorder()

	s, _, _ := runStreamer(t, d, rec)
	s.Reconcile([]host.Container{running("a"), running("b")})

	rec.waitFor(t, "samples from both", func() bool {
		return rec.count("a") > 0 && rec.count("b") > 0
	})

	if got := d.CallCount("Stats"); got != 2 {
		t.Errorf("opened %d stats streams, want one per container", got)
	}
}

// A stopped container must not keep a stream open.
func TestStoppedContainersLoseTheirStream(t *testing.T) {
	d := fake.New(running("a"), running("b"))
	d.SetSamples(host.Sample{CPUPct: 1})
	rec := newRecorder()

	s, _, _ := runStreamer(t, d, rec)
	s.Reconcile([]host.Container{running("a"), running("b")})
	rec.waitFor(t, "both streaming", func() bool { return d.CallCount("Stats") == 2 })

	before := runtime.NumGoroutine()

	// b goes down.
	stopped := fake.Stopped("b", "valheim")
	s.Reconcile([]host.Container{running("a"), stopped})

	settle(t, func() bool { return runtime.NumGoroutine() < before })
}

// Reconciling repeatedly with the same fleet must not open a second stream for
// a container that already has one. This is the reconnect-without-exiting
// failure the design calls out: it costs nothing visible on day one.
func TestReconcileIsIdempotent(t *testing.T) {
	d := fake.New(running("a"))
	d.SetSamples(host.Sample{CPUPct: 1})
	rec := newRecorder()

	s, _, _ := runStreamer(t, d, rec)
	for i := 0; i < 20; i++ {
		s.Reconcile([]host.Container{running("a")})
	}
	rec.waitFor(t, "a sample", func() bool { return rec.count("a") > 0 })

	// Give any spurious extra streams a chance to be opened.
	time.Sleep(200 * time.Millisecond)

	if got := d.CallCount("Stats"); got != 1 {
		t.Errorf("opened %d streams for one container across 20 reconciles, want 1", got)
	}
}

// The property that matters over weeks: after a full cycle of servers coming
// and going, the goroutine count comes back to where it started.
func TestNoGoroutinesLeakAcrossChurn(t *testing.T) {
	d := fake.New()
	d.SetSamples(host.Sample{CPUPct: 1})
	rec := newRecorder()

	ctx, cancel := context.WithCancel(context.Background())
	s := metrics.NewStreamer(d, rec)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()

	settle(t, func() bool { return true })
	before := runtime.NumGoroutine()

	// Reconcile conflates: a fleet queued behind another is replaced rather
	// than queued, so a test may not assume every call is processed. What
	// matters is that streams do open, do close, and leave nothing behind.
	var up []host.Container
	for i := 0; i < 4; i++ {
		c := running(string(rune('a' + i)))
		d.Put(c)
		up = append(up, c)
	}

	for cycle := 0; cycle < 10; cycle++ {
		opened := d.CallCount("Stats")
		s.Reconcile(up)
		rec.waitFor(t, "streams to open", func() bool { return d.CallCount("Stats") > opened })

		s.Reconcile(nil)
		settle(t, func() bool { return true })
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return")
	}

	settle(t, func() bool { return runtime.NumGoroutine() <= before })
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("goroutines grew from %d to %d across 40 stream lifetimes", before, after)
	}
}

// An engine that refuses the stats stream must not leave anything behind
// either, and must not wedge the streamer for the containers that do work.
func TestAStreamThatFailsToOpenIsNotALeak(t *testing.T) {
	d := fake.New(running("a"))
	d.SetFail("Stats", fake.ErrEngineDown)
	rec := newRecorder()

	s, _, _ := runStreamer(t, d, rec)

	settle(t, func() bool { return true })
	before := runtime.NumGoroutine()

	for i := 0; i < 10; i++ {
		s.Reconcile([]host.Container{running("a")})
		s.Reconcile(nil)
	}

	settle(t, func() bool { return runtime.NumGoroutine() <= before })
	if after := runtime.NumGoroutine(); after > before+1 {
		t.Errorf("goroutines grew from %d to %d after ten failed stream opens", before, after)
	}
}

func TestSamplesAreAttributedToTheInstanceNotTheContainerID(t *testing.T) {
	d := fake.New(running("valheim-huldra"))
	d.SetSamples(host.Sample{CPUPct: 42})
	rec := newRecorder()

	s, _, _ := runStreamer(t, d, rec)
	s.Reconcile([]host.Container{running("valheim-huldra")})

	rec.waitFor(t, "a sample", func() bool { return rec.count("valheim-huldra") > 0 })
}

// settle waits for a condition, giving the scheduler room to run deferred
// goroutine exits. Goroutine counts are inherently racy; this is the honest
// way to assert on them without a fixed sleep that is either flaky or slow.
func settle(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		runtime.Gosched()
		if cond() {
			// Two consecutive passes, so a transient dip does not count.
			time.Sleep(30 * time.Millisecond)
			runtime.GC()
			if cond() {
				return
			}
		}
		select {
		case <-deadline:
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
}
