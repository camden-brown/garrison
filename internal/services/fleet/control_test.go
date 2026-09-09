package fleet_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/host/fake"
	"github.com/camden-brown/garrison/internal/services/fleet"
)

func TestControllerStartsAndStops(t *testing.T) {
	d := fake.New(fake.Stopped("zomboid-main", "zomboid"))
	c := &fleet.Controller{Driver: d}
	ctx := context.Background()

	if err := c.Start(ctx, "zomboid-main", "c0ffeezomboid-main"); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := c.Stop(ctx, "zomboid-main", "c0ffeezomboid-main"); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	calls := d.Calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %v, want two", calls)
	}
	if !strings.HasPrefix(calls[0], "Start(") {
		t.Errorf("first call = %q, want Start", calls[0])
	}
	// The signal and grace are game facts. Until a plan supplies them the
	// defaults must still reach the driver, not an empty string and zero.
	if !strings.Contains(calls[1], fleet.DefaultStopSignal) {
		t.Errorf("stop call = %q, want the default signal %q", calls[1], fleet.DefaultStopSignal)
	}
	if !strings.Contains(calls[1], fleet.DefaultStopGrace.String()) {
		t.Errorf("stop call = %q, want the default grace %v", calls[1], fleet.DefaultStopGrace)
	}
}

func TestControllerHonoursConfiguredStopBehaviour(t *testing.T) {
	d := fake.New(fake.Running("valheim-huldra", "valheim", time.Now()))
	c := &fleet.Controller{Driver: d, StopSignal: "SIGINT", StopGrace: 90 * time.Second}

	if err := c.Stop(context.Background(), "valheim-huldra", "c0ffeevalheim-huldra"); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	call := d.Calls()[0]
	if !strings.Contains(call, "SIGINT") || !strings.Contains(call, "1m30s") {
		t.Errorf("stop call = %q, want SIGINT and 1m30s", call)
	}
}

// An alert has to be able to say which server, which operation, and why.
func TestControllerErrorsCarryTheInstanceAndOperation(t *testing.T) {
	d := fake.New(fake.Stopped("zomboid-main", "zomboid"))
	d.SetFail("Start", errors.New("port 16261 already allocated"))
	c := &fleet.Controller{Driver: d}

	err := c.Start(context.Background(), "zomboid-main", "c0ffeezomboid-main")
	if err == nil {
		t.Fatal("Start() error = nil, want a failure")
	}

	want := "zomboid-main: start: port 16261 already allocated"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}
