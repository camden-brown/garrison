package logs_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/host/fake"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/services/logs"
)

var start = time.Date(2026, 9, 9, 21, 0, 0, 0, time.UTC)

type collector struct {
	mu      sync.Mutex
	events  []model.Event
	batches int
	changed chan struct{}
}

func newCollector() *collector {
	return &collector{changed: make(chan struct{}, 256)}
}

func (c *collector) LogEventsRead(_ context.Context, _ string, events []model.Event) {
	c.mu.Lock()
	c.events = append(c.events, events...)
	c.batches++
	c.mu.Unlock()
	select {
	case c.changed <- struct{}{}:
	default:
	}
}

func (c *collector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

func (c *collector) batchCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.batches
}

func (c *collector) all() []model.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]model.Event(nil), c.events...)
}

func (c *collector) waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if cond() {
			return
		}
		select {
		case <-c.changed:
		case <-time.After(20 * time.Millisecond):
		case <-deadline:
			t.Fatalf("timed out waiting for %s (have %d events)", what, c.count())
		}
	}
}

// echoParser classifies every line as info, so a test can count lines without
// depending on a real game's format.
func echoParser(string) (func(string) model.Event, bool) {
	return func(line string) model.Event {
		return model.Event{Kind: model.KindInfo, Text: line, Raw: line}
	}, true
}

func running(instance, game string) host.Container {
	c := fake.Running(instance, game, start)
	c.State = model.StateRunning
	return c
}

func runStreamer(t *testing.T, s *logs.Streamer) {
	t.Helper()
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
}

func TestLinesBecomeEvents(t *testing.T) {
	d := fake.New(running("a", "valheim"))
	d.SetLogs("first\nsecond\nthird\n")
	col := newCollector()

	s := logs.NewStreamer(d, echoParser, col)
	s.FlushEvery = 10 * time.Millisecond
	runStreamer(t, s)
	s.Reconcile([]host.Container{running("a", "valheim")})

	col.waitFor(t, "three events", func() bool { return col.count() == 3 })

	got := col.all()
	for i, want := range []string{"first", "second", "third"} {
		if got[i].Text != want {
			t.Errorf("event %d = %q, want %q", i, got[i].Text, want)
		}
	}
}

// The reason batching exists: a crash-looping mod produces thousands of lines
// a second, and a mutation per line puts the store and the renderer under
// exactly the load the flood is warning about.
func TestAFloodIsBatchedNotStreamedLineByLine(t *testing.T) {
	const lines = 5000

	d := fake.New(running("a", "valheim"))
	d.SetLogs(strings.Repeat("a mod is unhappy\n", lines))
	col := newCollector()

	s := logs.NewStreamer(d, echoParser, col)
	s.FlushEvery = 20 * time.Millisecond
	runStreamer(t, s)
	s.Reconcile([]host.Container{running("a", "valheim")})

	col.waitFor(t, "every line", func() bool { return col.count() == lines })

	if batches := col.batchCount(); batches >= lines {
		t.Errorf("%d lines arrived in %d batches, want far fewer", lines, batches)
	}
}

// Dropped lines must cost nothing downstream — most of a game's output is
// noise its plugin has nothing to say about.
func TestDroppedLinesAreNotForwarded(t *testing.T) {
	d := fake.New(running("a", "valheim"))
	d.SetLogs("keep\ndrop\nkeep\n")
	col := newCollector()

	onlyKeep := func(string) (func(string) model.Event, bool) {
		return func(line string) model.Event {
			if line == "drop" {
				return model.Event{}
			}
			return model.Event{Kind: model.KindInfo, Text: line, Raw: line}
		}, true
	}

	s := logs.NewStreamer(d, onlyKeep, col)
	s.FlushEvery = 10 * time.Millisecond
	runStreamer(t, s)
	s.Reconcile([]host.Container{running("a", "valheim")})

	col.waitFor(t, "two events", func() bool { return col.count() == 2 })
	time.Sleep(60 * time.Millisecond)

	if got := col.count(); got != 2 {
		t.Errorf("got %d events, want the dropped line excluded", got)
	}
}

// A game with no plugin still has output worth showing.
func TestUnknownGameStillProducesLines(t *testing.T) {
	d := fake.New(running("a", "notagame"))
	d.SetLogs("something happened\n")
	col := newCollector()

	none := func(string) (func(string) model.Event, bool) { return nil, false }

	s := logs.NewStreamer(d, none, col)
	s.FlushEvery = 10 * time.Millisecond
	runStreamer(t, s)
	s.Reconcile([]host.Container{running("a", "notagame")})

	col.waitFor(t, "the line", func() bool { return col.count() == 1 })
	if got := col.all()[0].Raw; got != "something happened" {
		t.Errorf("raw = %q, want the line preserved", got)
	}
}

// A stack trace or a mod dumping a table exceeds bufio's 64 KiB default, and
// the default is a hard error that ends the stream rather than a truncation.
func TestVeryLongLinesDoNotKillTheStream(t *testing.T) {
	long := strings.Repeat("x", 200*1024)

	d := fake.New(running("a", "valheim"))
	d.SetLogs(long + "\nafter the long line\n")
	col := newCollector()

	s := logs.NewStreamer(d, echoParser, col)
	s.FlushEvery = 10 * time.Millisecond
	runStreamer(t, s)
	s.Reconcile([]host.Container{running("a", "valheim")})

	col.waitFor(t, "the line after the long one", func() bool { return col.count() == 2 })
	if got := col.all()[1].Text; got != "after the long line" {
		t.Errorf("second event = %q, want the stream to have continued", got)
	}
}

// A container going up and down repeatedly must open exactly one follow per
// time it comes up, and none while it is down.
func TestOneFollowPerTimeAContainerComesUp(t *testing.T) {
	const cycles = 3

	d := fake.New(running("a", "valheim"))
	d.SetLogs("line\n")
	col := newCollector()

	s := logs.NewStreamer(d, echoParser, col)
	s.FlushEvery = 10 * time.Millisecond
	runStreamer(t, s)

	for i := 1; i <= cycles; i++ {
		s.Reconcile([]host.Container{running("a", "valheim")})
		col.waitFor(t, "the stream to open", func() bool { return d.CallCount("Logs") >= i })

		s.Reconcile([]host.Container{fake.Stopped("a", "valheim")})
		// Give a spurious reopen a chance to happen while it is down.
		time.Sleep(50 * time.Millisecond)

		if opened := d.CallCount("Logs"); opened != i {
			t.Fatalf("after %d cycles %d streams had been opened, want %d", i, opened, i)
		}
	}
}

// Reconciling repeatedly with an unchanged fleet must not reopen anything.
func TestRepeatedReconcileDoesNotReopen(t *testing.T) {
	d := fake.New(running("a", "valheim"))
	d.SetLogs("line\n")
	col := newCollector()

	s := logs.NewStreamer(d, echoParser, col)
	s.FlushEvery = 10 * time.Millisecond
	runStreamer(t, s)

	s.Reconcile([]host.Container{running("a", "valheim")})
	col.waitFor(t, "the first line", func() bool { return col.count() >= 1 })

	for i := 0; i < 20; i++ {
		s.Reconcile([]host.Container{running("a", "valheim")})
	}
	time.Sleep(100 * time.Millisecond)

	if opened := d.CallCount("Logs"); opened != 1 {
		t.Errorf("opened %d streams across 21 reconciles of the same fleet, want 1", opened)
	}
}
