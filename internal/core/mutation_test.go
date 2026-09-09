package core

import (
	"errors"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/model"
)

var at = time.Date(2026, 9, 9, 21, 7, 0, 0, time.UTC)

// Reducers are snapshot in, mutation in, snapshot out. No Docker, no clock, no
// goroutines — which is what makes the interesting state transitions cheap
// enough to test exhaustively.
func apply(s Snapshot, ms ...Mutation) Snapshot { return Reduce(s, ms...) }

func observed(cs ...host.Container) FleetObserved {
	return FleetObserved{At: at, Containers: cs}
}

func TestFleetObservedSortsByName(t *testing.T) {
	got := apply(Snapshot{}, observed(
		host.Container{Instance: "zomboid-main"},
		host.Container{Instance: "aardvark"},
		host.Container{Instance: "valheim-huldra"},
	))

	want := []string{"aardvark", "valheim-huldra", "zomboid-main"}
	if len(got.Servers) != len(want) {
		t.Fatalf("got %d servers, want %d", len(got.Servers), len(want))
	}
	for i, name := range want {
		if got.Servers[i].Name != name {
			t.Errorf("server %d = %q, want %q", i, got.Servers[i].Name, name)
		}
	}
}

func TestFleetObservedMarksTheEngineHealthy(t *testing.T) {
	got := apply(Snapshot{Engine: Engine{Err: "stale"}}, observed())
	if !got.Engine.OK {
		t.Error("Engine.OK = false after a successful observation")
	}
	if got.Engine.Err != "" {
		t.Errorf("Engine.Err = %q, want it cleared", got.Engine.Err)
	}
	if !got.Engine.LastOK.Equal(at) {
		t.Errorf("Engine.LastOK = %v, want %v", got.Engine.LastOK, at)
	}
}

// Busy is Garrison's own knowledge, not the engine's. A poll landing mid-stop
// must not wipe the "stopping…" the operator is looking at.
func TestFleetObservedPreservesBusy(t *testing.T) {
	s := apply(Snapshot{},
		observed(host.Container{Instance: "zomboid-main", State: model.StateRunning}),
		OperationBegan{At: at, Server: "zomboid-main", Op: OpStop},
		observed(host.Container{Instance: "zomboid-main", State: model.StateRunning}),
	)

	srv, ok := s.Server("zomboid-main")
	if !ok {
		t.Fatal("server missing")
	}
	if srv.Busy != OpStop {
		t.Errorf("Busy = %q, want %q", srv.Busy, OpStop)
	}
}

func TestFleetObservedDropsContainersThatVanished(t *testing.T) {
	s := apply(Snapshot{},
		observed(host.Container{Instance: "a"}, host.Container{Instance: "b"}),
		observed(host.Container{Instance: "a"}),
	)
	if len(s.Servers) != 1 || s.Servers[0].Name != "a" {
		t.Errorf("servers = %+v, want just a", s.Servers)
	}
}

func TestDetailForCleanExit(t *testing.T) {
	s := apply(Snapshot{}, observed(host.Container{Instance: "a", State: model.StateStopped}))
	if got := s.Servers[0].Detail; got != "exit 0" {
		t.Errorf("Detail = %q, want %q", got, "exit 0")
	}
}

// The honest state. A dashboard that redraws a running fleet as stopped
// because it lost its socket is worse than one that admits it cannot see.
func TestFleetUnobservableGoesUnknownWithoutEmptyingTheFleet(t *testing.T) {
	s := apply(Snapshot{},
		observed(
			host.Container{Instance: "zomboid-main", State: model.StateRunning},
			host.Container{Instance: "valheim-huldra", State: model.StateStopped},
		),
		FleetUnobservable{At: at, Err: errors.New("docker daemon not running")},
	)

	if len(s.Servers) != 2 {
		t.Fatalf("got %d servers, want the fleet kept", len(s.Servers))
	}
	for _, srv := range s.Servers {
		if srv.State != model.StateUnknown {
			t.Errorf("%s state = %v, want unknown", srv.Name, srv.State)
		}
		if srv.Detail != "engine unreachable" {
			t.Errorf("%s detail = %q", srv.Name, srv.Detail)
		}
	}
	if s.Engine.OK {
		t.Error("Engine.OK = true after a failed observation")
	}
}

// The poller retries every few seconds. A notice per attempt buries everything
// else within a minute of Docker Desktop restarting for an update.
func TestFleetUnobservableNoticesOnlyTheTransition(t *testing.T) {
	down := FleetUnobservable{At: at, Err: errors.New("boom")}

	s := apply(Snapshot{}, observed(), down, down, down)
	if got := len(s.Notices); got != 1 {
		t.Errorf("got %d notices after three failed polls, want 1", got)
	}

	// Recovering and failing again is a new fact and does notice.
	s = apply(s, observed(), down)
	if got := len(s.Notices); got != 2 {
		t.Errorf("got %d notices after a recovery and a second outage, want 2", got)
	}
}

func TestOperationEndedClearsBusyAndReportsFailure(t *testing.T) {
	s := apply(Snapshot{},
		observed(host.Container{Instance: "zomboid-main", State: model.StateStopped}),
		OperationBegan{At: at, Server: "zomboid-main", Op: OpStart},
		OperationEnded{
			At:     at,
			Server: "zomboid-main",
			Op:     OpStart,
			Err:    errors.New("zomboid-main: start: port 16261 already allocated"),
		},
	)

	srv, _ := s.Server("zomboid-main")
	if srv.Busy != OpNone {
		t.Errorf("Busy = %q, want cleared", srv.Busy)
	}
	if len(s.Notices) != 1 {
		t.Fatalf("got %d notices, want 1", len(s.Notices))
	}
	if s.Notices[0].Level != LevelError {
		t.Errorf("notice level = %v, want error", s.Notices[0].Level)
	}
	if s.Notices[0].Text != "zomboid-main: start: port 16261 already allocated" {
		t.Errorf("notice text = %q", s.Notices[0].Text)
	}
}

// The engine reports what the container actually did. Writing an optimistic
// "running" here is how a fleet view starts lying.
func TestOperationEndedDoesNotInventState(t *testing.T) {
	s := apply(Snapshot{},
		observed(host.Container{Instance: "a", State: model.StateStopped}),
		OperationBegan{At: at, Server: "a", Op: OpStart},
		OperationEnded{At: at, Server: "a", Op: OpStart},
	)
	if got := s.Servers[0].State; got != model.StateStopped {
		t.Errorf("state = %v, want it left to the next poll", got)
	}
}

func TestNoticesAreBounded(t *testing.T) {
	s := Snapshot{}
	for i := 0; i < maxNotices*2; i++ {
		s = apply(s, NoticeRaised{At: at, Text: "spam"})
	}
	if len(s.Notices) != maxNotices {
		t.Errorf("got %d notices, want the cap of %d", len(s.Notices), maxNotices)
	}
}

// A published snapshot must never change. A reducer writing through to a
// shared backing array would mutate a value the TUI is halfway through
// rendering.
func TestReducersDoNotMutatePublishedSnapshots(t *testing.T) {
	before := apply(Snapshot{}, observed(
		host.Container{Instance: "a", State: model.StateRunning},
		host.Container{Instance: "b", State: model.StateRunning},
	))

	after := apply(before, OperationBegan{At: at, Server: "a", Op: OpStop})

	if before.Servers[0].Busy != OpNone {
		t.Error("the earlier snapshot was mutated in place")
	}
	if after.Servers[0].Busy != OpStop {
		t.Error("the new snapshot did not record the operation")
	}
}

func TestCounts(t *testing.T) {
	s := apply(Snapshot{}, observed(
		host.Container{Instance: "a", State: model.StateRunning},
		host.Container{Instance: "b", State: model.StateRestarting},
		host.Container{Instance: "c", State: model.StateStopped},
		host.Container{Instance: "d", State: model.StateCrashed},
		host.Container{Instance: "e", State: model.StateUnknown},
	))

	up, down, unknown := s.Counts()
	if up != 2 || down != 2 || unknown != 1 {
		t.Errorf("Counts() = (%d, %d, %d), want (2, 2, 1)", up, down, unknown)
	}
}

func TestUptimeIsZeroWhenNotRunning(t *testing.T) {
	stopped := Server{State: model.StateStopped, Started: at.Add(-time.Hour)}
	if got := stopped.Uptime(at); got != 0 {
		t.Errorf("Uptime() = %v for a stopped server, want 0", got)
	}

	running := Server{State: model.StateRunning, Started: at.Add(-time.Hour)}
	if got := running.Uptime(at); got != time.Hour {
		t.Errorf("Uptime() = %v, want 1h", got)
	}
}
