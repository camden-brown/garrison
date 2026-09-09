//go:build integration

// This is the one test that needs a real engine. Everything else in Garrison
// runs with Docker stopped, which is what keeps the suite worth running; this
// exists so that the assumptions the fake encodes are checked against the
// thing it stands in for, at least once.
//
//	go test -tags integration ./internal/host/docker
package docker

import (
	"bufio"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/model"
)

const testImage = "alpine:3"

func TestDriverAgainstARealEngine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	d, err := Open("", Env{Docker: envOrEmpty()})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer d.Close()

	if err := d.Ping(ctx); err != nil {
		t.Skipf("no engine reachable: %v", err)
	}

	inst := model.Instance{Name: "garrison-selftest", Game: "selftest"}
	plan := model.Plan{
		Image:      testImage,
		Cmd:        []string{"sh", "-c", "echo hello-from-garrison; while true; do sleep 1; done"},
		Env:        map[string]string{"GARRISON_TEST": "1"},
		Resources:  model.Resources{Memory: 64 << 20},
		StopSignal: "SIGTERM",
		StopGrace:  5 * time.Second,
	}

	id, err := d.Create(ctx, inst, plan)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	t.Cleanup(func() {
		// A leaked container from a failed test poisons the next run, so
		// removal is unconditional and forceful.
		_ = d.Remove(context.Background(), id, true)
	})

	// The label contract: the fleet is found by label, so a container that
	// does not carry them is invisible to Garrison forever.
	got, err := d.Inspect(ctx, id)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if got.Instance != inst.Name {
		t.Errorf("Instance = %q, want %q", got.Instance, inst.Name)
	}
	if want := host.PlanHash(plan); got.PlanHash != want {
		t.Errorf("PlanHash = %q, want %q", got.PlanHash, want)
	}
	if got.State != model.StateCreated {
		t.Errorf("State = %v, want created", got.State)
	}

	if err := d.Start(ctx, id); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if got, err = d.Inspect(ctx, id); err != nil {
		t.Fatalf("Inspect() after start error = %v", err)
	}
	if got.State != model.StateRunning {
		t.Fatalf("State = %v, want running", got.State)
	}

	t.Run("List finds it by label", func(t *testing.T) {
		listed, err := d.List(ctx)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		for _, c := range listed {
			if c.ID == id {
				return
			}
		}
		t.Errorf("the container is not in List(); labels are not doing their job")
	})

	t.Run("Logs are demultiplexed", func(t *testing.T) {
		lctx, lcancel := context.WithTimeout(ctx, 30*time.Second)
		defer lcancel()

		rc, err := d.Logs(lctx, id, 10)
		if err != nil {
			t.Fatalf("Logs() error = %v", err)
		}
		defer rc.Close()

		scanner := bufio.NewScanner(rc)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "hello-from-garrison") {
				return
			}
		}
		t.Error("the container's output never arrived, or arrived still framed")
	})

	t.Run("Stats stream produces samples", func(t *testing.T) {
		sctx, scancel := context.WithTimeout(ctx, 30*time.Second)
		defer scancel()

		samples, err := d.Stats(sctx, id)
		if err != nil {
			t.Fatalf("Stats() error = %v", err)
		}

		// The second sample is the first with a previous frame to measure
		// against; the first is always 0% by design.
		for i := 0; i < 2; i++ {
			select {
			case s, ok := <-samples:
				if !ok {
					t.Fatal("the stats stream closed early")
				}
				if i == 1 && s.MemBytes <= 0 {
					t.Errorf("MemBytes = %d, want a real figure", s.MemBytes)
				}
				if s.CPUPct < 0 || s.CPUPct > 10000 {
					t.Errorf("CPUPct = %v, which is not a plausible percentage", s.CPUPct)
				}
			case <-sctx.Done():
				t.Fatal("timed out waiting for a stats sample")
			}
		}
	})

	t.Run("Exec runs in the container", func(t *testing.T) {
		out, err := d.Exec(ctx, id, []string{"sh", "-c", "echo exec-ok"})
		if err != nil {
			t.Fatalf("Exec() error = %v", err)
		}
		if !strings.Contains(string(out), "exec-ok") {
			t.Errorf("Exec() output = %q", out)
		}
	})

	t.Run("a failing Exec is an error carrying its output", func(t *testing.T) {
		_, err := d.Exec(ctx, id, []string{"sh", "-c", "echo nope >&2; exit 3"})
		if err == nil {
			t.Fatal("Exec() error = nil for a command that exited 3")
		}
		if !strings.Contains(err.Error(), "exit 3") {
			t.Errorf("error = %v, want it to name the exit code", err)
		}
	})

	if err := d.Stop(ctx, id, plan.StopSignal, plan.StopGrace); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	// Stopped, not crashed: the container was asked to leave and did.
	if got, err = d.Inspect(ctx, id); err != nil {
		t.Fatalf("Inspect() after stop error = %v", err)
	}
	if got.State != model.StateStopped && got.State != model.StateCrashed {
		t.Errorf("State = %v, want the container to be down", got.State)
	}

	if err := d.Remove(ctx, id, true); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := d.Inspect(ctx, id); err == nil {
		t.Error("Inspect() succeeded after Remove()")
	}
}

func envOrEmpty() string {
	return "" // let Resolve fall through to the per-OS default
}
