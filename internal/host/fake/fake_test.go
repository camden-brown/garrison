package fake_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/host/fake"
	"github.com/camden-brown/garrison/internal/model"
)

var at = time.Date(2026, 9, 9, 21, 7, 0, 0, time.UTC)

// The fake is only worth having if it behaves like the thing it stands in for.
func TestStartAndStopMoveState(t *testing.T) {
	d := fake.New(fake.Stopped("a", "valheim"))
	d.SetClock(func() time.Time { return at })
	ctx := context.Background()

	if err := d.Start(ctx, "c0ffeea"); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	got, err := d.Inspect(ctx, "c0ffeea")
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if got.State != model.StateRunning {
		t.Errorf("state = %v, want running", got.State)
	}
	if !got.Started.Equal(at) {
		t.Errorf("Started = %v, want %v", got.Started, at)
	}

	if err := d.Stop(ctx, "c0ffeea", "SIGTERM", time.Minute); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	got, _ = d.Inspect(ctx, "c0ffeea")
	if got.State != model.StateStopped {
		t.Errorf("state = %v, want stopped", got.State)
	}
}

// A zero Driver must not panic on first use — a test double that traps on its
// own zero value is worse than none.
func TestZeroValueIsUsable(t *testing.T) {
	var d fake.Driver
	d.Put(fake.Stopped("a", "valheim"))

	got, err := d.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d containers, want 1", len(got))
	}
}

func TestDownFailsEverything(t *testing.T) {
	d := fake.New(fake.Running("a", "valheim", at))
	d.SetDown(true)

	if _, err := d.List(context.Background()); err == nil {
		t.Error("List() error = nil while down")
	}
	if err := d.Ping(context.Background()); err == nil {
		t.Error("Ping() error = nil while down")
	}
}

func TestRemoveDropsFromTheList(t *testing.T) {
	d := fake.New(fake.Running("a", "valheim", at), fake.Running("b", "valheim", at))
	ctx := context.Background()

	if err := d.Remove(ctx, "c0ffeea", false); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	got, _ := d.List(ctx)
	if len(got) != 1 || got[0].Instance != "b" {
		t.Errorf("list = %+v, want just b", got)
	}
}

func TestCreateStampsThePlanHash(t *testing.T) {
	d := fake.New()
	plan := model.Plan{Image: "valheim:1", Env: map[string]string{"A": "B"}}

	id, err := d.Create(context.Background(), model.Instance{Name: "a", Game: "valheim"}, plan)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	got, _ := d.Inspect(context.Background(), id)
	if got.PlanHash != host.PlanHash(plan) {
		t.Errorf("PlanHash = %q, want %q", got.PlanHash, host.PlanHash(plan))
	}
}

// The fake exists to be hammered from the goroutines under test. Under -race
// this is the test that says its locking is right — and that reading the clock
// inside a state transition does not deadlock against its own mutex.
func TestConcurrentUseIsSafe(t *testing.T) {
	d := fake.New(fake.Stopped("a", "valheim"))
	d.SetClock(time.Now)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = d.List(ctx)
				_ = d.Start(ctx, "c0ffeea")
				_ = d.Stop(ctx, "c0ffeea", "SIGTERM", time.Second)
				_ = d.Ping(ctx)
				d.SetDown(j%10 == 0)
				_ = d.Calls()
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent use deadlocked")
	}
}
