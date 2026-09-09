package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/model"
)

func testStore(t *testing.T, ctl Control) (*Store, context.Context) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	s := New(Options{Control: ctl, Now: func() time.Time { return at }})

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

type recordingControl struct {
	started chan string
	stopped chan string
	err     error
}

func (c *recordingControl) Start(ctx context.Context, instance, id string) error {
	c.started <- instance
	return c.err
}

func (c *recordingControl) Stop(ctx context.Context, instance, id string) error {
	c.stopped <- instance
	return c.err
}

func (c *recordingControl) StopGrace(string) time.Duration { return 90 * time.Second }

func newControl() *recordingControl {
	return &recordingControl{
		started: make(chan string, 4),
		stopped: make(chan string, 4),
	}
}

func TestStartMarksBusyThenClears(t *testing.T) {
	ctl := newControl()
	s, ctx := testStore(t, ctl)
	sub := s.Subscribe()

	s.FleetObserved(ctx, at, []host.Container{{Instance: "a", State: model.StateStopped}})
	waitFor(t, sub, "the fleet", func(s Snapshot) bool { return len(s.Servers) == 1 })

	s.Start(ctx, "a")

	select {
	case got := <-ctl.started:
		if got != "a" {
			t.Errorf("started %q, want a", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Control.Start was never called")
	}

	waitFor(t, sub, "busy to clear", func(s Snapshot) bool {
		srv, ok := s.Server("a")
		return ok && srv.Busy == OpNone
	})
}

func TestFailedOperationBecomesANotice(t *testing.T) {
	ctl := newControl()
	ctl.err = errors.New("a: start: port 16261 already allocated")
	s, ctx := testStore(t, ctl)
	sub := s.Subscribe()

	s.FleetObserved(ctx, at, []host.Container{{Instance: "a", State: model.StateStopped}})
	waitFor(t, sub, "the fleet", func(s Snapshot) bool { return len(s.Servers) == 1 })

	s.Start(ctx, "a")

	snap := waitFor(t, sub, "the failure notice", func(s Snapshot) bool { return len(s.Notices) > 0 })
	if snap.Notices[0].Text != "a: start: port 16261 already allocated" {
		t.Errorf("notice = %q", snap.Notices[0].Text)
	}
}

// The answer arrives as a sentence on the status bar rather than as a Docker
// error two seconds later.
func TestOperationsThatCannotSucceedAreRefusedUpFront(t *testing.T) {
	tests := []struct {
		name  string
		state model.State
		op    func(*Store, context.Context)
		want  string
	}{
		{
			name:  "starting a running server",
			state: model.StateRunning,
			op:    func(s *Store, ctx context.Context) { s.Start(ctx, "a") },
			want:  "a: start: already running",
		},
		{
			name:  "stopping a stopped server",
			state: model.StateStopped,
			op:    func(s *Store, ctx context.Context) { s.Stop(ctx, "a") },
			want:  "a: stop: already stopped",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctl := newControl()
			s, ctx := testStore(t, ctl)
			sub := s.Subscribe()

			s.FleetObserved(ctx, at, []host.Container{{Instance: "a", State: tt.state}})
			waitFor(t, sub, "the fleet", func(s Snapshot) bool { return len(s.Servers) == 1 })

			tt.op(s, ctx)

			snap := waitFor(t, sub, "the refusal", func(s Snapshot) bool { return len(s.Notices) > 0 })
			if snap.Notices[0].Text != tt.want {
				t.Errorf("notice = %q, want %q", snap.Notices[0].Text, tt.want)
			}
			if len(ctl.started)+len(ctl.stopped) != 0 {
				t.Error("the driver was called for an operation that should have been refused")
			}
		})
	}
}

// Unknown means the state is stale, not that the server is gone. The honest
// response to "start it anyway" is to try.
func TestUnknownStateDoesNotBlockAnOperation(t *testing.T) {
	ctl := newControl()
	s, ctx := testStore(t, ctl)
	sub := s.Subscribe()

	s.FleetObserved(ctx, at, []host.Container{{Instance: "a", State: model.StateUnknown}})
	waitFor(t, sub, "the fleet", func(s Snapshot) bool { return len(s.Servers) == 1 })

	s.Start(ctx, "a")

	select {
	case <-ctl.started:
	case <-time.After(2 * time.Second):
		t.Fatal("an operation on an unknown-state server was refused")
	}
}

func TestOperationOnAnUnknownServerIsReported(t *testing.T) {
	s, ctx := testStore(t, newControl())
	sub := s.Subscribe()

	s.Start(ctx, "nope")

	snap := waitFor(t, sub, "the notice", func(s Snapshot) bool { return len(s.Notices) > 0 })
	if snap.Notices[0].Text != "nope: start: no such server" {
		t.Errorf("notice = %q", snap.Notices[0].Text)
	}
}
