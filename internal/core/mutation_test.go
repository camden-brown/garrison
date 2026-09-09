package core

import (
	"errors"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tasks"
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
		running("zomboid-main", tasks.KindStop),
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

// running is a task in flight against a server.
func running(server string, kind tasks.Kind) TaskProgressed {
	return TaskProgressed{At: at, Progress: tasks.Progress{
		ID: string(kind) + "-1", Server: server, Kind: kind,
		State: tasks.StateRunning, Steps: []string{string(kind)},
		Est: []time.Duration{0},
	}}
}

// stopping is a stop task that has read the game's plan and knows its grace.
func stopping(server string, grace time.Duration) TaskProgressed {
	p := running(server, tasks.KindStop)
	p.Progress.Est = []time.Duration{grace}
	return p
}

// finished is a task that completed against a server.
func finished(server string, kind tasks.Kind) TaskProgressed {
	return TaskProgressed{At: at, Progress: tasks.Progress{
		ID: string(kind) + "-1", Server: server, Kind: kind,
		State: tasks.StateDone, Steps: []string{string(kind)}, Cursor: 1,
	}}
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

func TestFailedTaskClearsBusyAndReportsIt(t *testing.T) {
	s := apply(Snapshot{},
		observed(host.Container{Instance: "zomboid-main", State: model.StateStopped}),
		running("zomboid-main", tasks.KindStart),
		TaskProgressed{At: at, Progress: tasks.Progress{
			ID: "start-1", Server: "zomboid-main", Kind: tasks.KindStart,
			State: tasks.StateFailed,
			Err:   "zomboid-main: start: port 16261 already allocated",
		}},
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
func TestAFinishedTaskDoesNotInventState(t *testing.T) {
	s := apply(Snapshot{},
		observed(host.Container{Instance: "a", State: model.StateStopped}),
		running("a", tasks.KindStart),
		finished("a", tasks.KindStart),
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

	after := apply(before, running("a", tasks.KindStop))

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

// A stop Garrison asked for is not a crash, even when the exit code is
// identical to one. This is the zomboid-main case: a server that ignores
// SIGTERM is killed when its grace runs out and exits 137.
func TestRequestedStopIsNotACrash(t *testing.T) {
	killed := host.Container{
		Instance: "zomboid-main",
		State:    model.StateCrashed,
		ExitCode: 137,
	}

	s := apply(Snapshot{},
		observed(host.Container{Instance: "zomboid-main", State: model.StateRunning}),
		stopping("zomboid-main", 60*time.Second),
		finished("zomboid-main", tasks.KindStop),
		observed(killed),
	)

	srv, _ := s.Server("zomboid-main")
	if srv.State != model.StateStopped {
		t.Errorf("state = %v, want stopped — Garrison asked for this", srv.State)
	}
	if srv.Detail != "killed after 60s grace" {
		t.Errorf("detail = %q, want it to name the grace it waited", srv.Detail)
	}
	if srv.ExitCode != 137 {
		t.Errorf("ExitCode = %d, want 137 kept for the record", srv.ExitCode)
	}
}

// With no grace recorded — the stop was requested by an earlier process, so
// the number died with it — say what happened without inventing one.
func TestKillWithNoRecordedGraceDoesNotInventANumber(t *testing.T) {
	s := apply(Snapshot{},
		observed(host.Container{Instance: "a", State: model.StateRunning}),
		finished("a", tasks.KindStop),
		observed(host.Container{Instance: "a", State: model.StateCrashed, ExitCode: 137}),
	)

	if got := s.Servers[0].Detail; got != "killed — did not stop in time" {
		t.Errorf("detail = %q, want no fabricated duration", got)
	}
}

// The in-flight message and the after-the-fact reason must name the same
// number the same way, or the operator reads them as two different events.
func TestBudget(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{5 * time.Second, "5s"},
		{60 * time.Second, "60s"},
		{90 * time.Second, "90s"},
		{2 * time.Minute, "2m"},
		{120 * time.Second, "2m"},
		{150 * time.Second, "2m30s"},
		{5 * time.Minute, "5m"},
	}

	for _, tt := range tests {
		if got := Budget(tt.in); got != tt.want {
			t.Errorf("Budget(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// The grace reaches the snapshot the moment the stop starts, because that is
// when the view needs to say how long the wait will be.
func TestStopGraceIsVisibleWhileStopping(t *testing.T) {
	s := apply(Snapshot{},
		observed(host.Container{Instance: "a", State: model.StateRunning}),
		stopping("a", 120*time.Second),
	)

	srv, _ := s.Server("a")
	if srv.Busy != OpStop {
		t.Fatalf("Busy = %q, want stop", srv.Busy)
	}
	if srv.StopGrace != 120*time.Second {
		t.Errorf("StopGrace = %v, want 2m", srv.StopGrace)
	}
}

// The same exit code with nobody asking is exactly what it looks like.
func TestUnrequestedKillIsStillACrash(t *testing.T) {
	s := apply(Snapshot{}, observed(host.Container{
		Instance: "zomboid-main",
		State:    model.StateCrashed,
		Detail:   "exit 137",
		ExitCode: 137,
	}))

	if got := s.Servers[0].State; got != model.StateCrashed {
		t.Errorf("state = %v, want crashed", got)
	}
}

// The kernel stepping in is news regardless of what we were doing at the time.
func TestOOMDuringARequestedStopStaysACrash(t *testing.T) {
	s := apply(Snapshot{},
		observed(host.Container{Instance: "a", State: model.StateRunning}),
		finished("a", tasks.KindStop),
		observed(host.Container{
			Instance:  "a",
			State:     model.StateCrashed,
			Detail:    "OOM killed",
			ExitCode:  137,
			OOMKilled: true,
		}),
	)

	srv, _ := s.Server("a")
	if srv.State != model.StateCrashed {
		t.Errorf("state = %v, want crashed — the kernel killed it", srv.State)
	}
	if srv.Detail != "OOM killed" {
		t.Errorf("detail = %q, want the OOM reason kept", srv.Detail)
	}
}

// Starting it again retires the stop, so the next crash reports as one.
func TestStartingRetiresTheStopRequest(t *testing.T) {
	s := apply(Snapshot{},
		observed(host.Container{Instance: "a", State: model.StateRunning}),
		finished("a", tasks.KindStop),
		running("a", tasks.KindStart),
		observed(host.Container{Instance: "a", State: model.StateCrashed, ExitCode: 137}),
	)

	if got := s.Servers[0].State; got != model.StateCrashed {
		t.Errorf("state = %v, want crashed after a restart", got)
	}
}

// Coming back up on its own clears it too, without an explicit start.
func TestObservingItRunningClearsTheStopRequest(t *testing.T) {
	s := apply(Snapshot{},
		observed(host.Container{Instance: "a", State: model.StateRunning}),
		finished("a", tasks.KindStop),
		observed(host.Container{Instance: "a", State: model.StateRunning}),
		observed(host.Container{Instance: "a", State: model.StateCrashed, ExitCode: 137}),
	)

	if got := s.Servers[0].State; got != model.StateCrashed {
		t.Errorf("state = %v, want crashed once it had come back up", got)
	}
}

// A failed stop leaves nothing behind: the server never went down, so a later
// crash is a crash.
func TestFailedStopDoesNotCountAsRequested(t *testing.T) {
	s := apply(Snapshot{},
		observed(host.Container{Instance: "a", State: model.StateRunning}),
		TaskProgressed{At: at, Progress: tasks.Progress{
			ID: "stop-1", Server: "a", Kind: tasks.KindStop, State: tasks.StateFailed,
			Err: "a: stop: no such container",
		}},
		observed(host.Container{Instance: "a", State: model.StateCrashed, ExitCode: 137}),
	)

	if got := s.Servers[0].State; got != model.StateCrashed {
		t.Errorf("state = %v, want crashed after a failed stop", got)
	}
}

// Samples accumulate into the history the dashboard draws.
func TestStatsSampledBuildsHistory(t *testing.T) {
	s := apply(Snapshot{}, observed(host.Container{Instance: "a", State: model.StateRunning}))

	for i := 0; i < 5; i++ {
		s = apply(s, StatsSampled{
			At:     at.Add(time.Duration(i) * time.Second),
			Server: "a",
			Sample: host.Sample{CPUPct: float64(i * 10), MemBytes: int64(i) << 20, MemLimit: 4 << 30},
		})
	}

	srv, _ := s.Server("a")
	if got := len(srv.CPU.Hot); got != 5 {
		t.Fatalf("CPU history has %d points, want 5", got)
	}
	if last, _ := srv.CPU.Last(); last.Mean != 40 {
		t.Errorf("newest CPU = %v, want 40", last.Mean)
	}
	if srv.MemLimit != 4<<30 {
		t.Errorf("MemLimit = %d, want it carried from the sample", srv.MemLimit)
	}
}

// A poll lands every five seconds and rebuilds the server list from the
// engine. It must not take four seconds of samples with it.
func TestPollDoesNotDiscardHistory(t *testing.T) {
	s := apply(Snapshot{}, observed(host.Container{Instance: "a", State: model.StateRunning}))
	for i := 0; i < 5; i++ {
		s = apply(s, StatsSampled{At: at, Server: "a", Sample: host.Sample{CPUPct: 1}})
	}

	s = apply(s, observed(host.Container{Instance: "a", State: model.StateRunning}))

	srv, _ := s.Server("a")
	if got := len(srv.CPU.Hot); got != 5 {
		t.Errorf("the poll left %d history points, want 5 kept", got)
	}
}

// The same immutability the History type guarantees has to survive the
// reducer: an earlier snapshot the TUI is still rendering must not change.
func TestStatsSampledDoesNotMutateEarlierSnapshots(t *testing.T) {
	before := apply(Snapshot{},
		observed(host.Container{Instance: "a", State: model.StateRunning}),
		StatsSampled{At: at, Server: "a", Sample: host.Sample{CPUPct: 1}},
	)
	after := apply(before, StatsSampled{At: at, Server: "a", Sample: host.Sample{CPUPct: 2}})

	if got := len(before.Servers[0].CPU.Hot); got != 1 {
		t.Errorf("the earlier snapshot now has %d points, want 1", got)
	}
	if got := len(after.Servers[0].CPU.Hot); got != 2 {
		t.Errorf("the newer snapshot has %d points, want 2", got)
	}
}

// A sample carrying its own timestamp is stamped with it, not with when the
// store happened to apply it.
func TestSampleKeepsItsOwnTimestamp(t *testing.T) {
	sampled := at.Add(-3 * time.Second)
	s := apply(Snapshot{},
		observed(host.Container{Instance: "a", State: model.StateRunning}),
		StatsSampled{At: at, Server: "a", Sample: host.Sample{At: sampled, CPUPct: 7}},
	)

	last, _ := s.Servers[0].CPU.Last()
	if !last.At.Equal(sampled) {
		t.Errorf("point stamped %v, want the sample's own %v", last.At, sampled)
	}
}

func logs(server string, events ...model.Event) LogEventsRead {
	return LogEventsRead{At: at, Server: server, Events: events}
}

func connect(id string) model.Event {
	return model.Event{Kind: model.KindConnect, At: at, SteamID: id}
}

func join(name string) model.Event {
	return model.Event{Kind: model.KindJoin, At: at, Player: name}
}

func leave(id string) model.Event {
	return model.Event{Kind: model.KindLeave, At: at, SteamID: id}
}

func withServer(name string) Snapshot {
	return apply(Snapshot{}, observed(host.Container{Instance: name, State: model.StateRunning}))
}

func names(players []model.Player) []string {
	out := make([]string, len(players))
	for i, p := range players {
		out[i] = p.Name
	}
	return out
}

// The whole point of doing Valheim first: it reports identity in pieces, so
// the roster has to correlate a Steam id seen at connect time with a name that
// arrives up to a minute later, and then resolve a departure that names only
// the id again.
func TestRosterBindsANameToAConnection(t *testing.T) {
	s := apply(withServer("a"), logs("a",
		connect("76561190000000001"),
		join("Dalinar"),
	))

	srv, _ := s.Server("a")
	if got := names(srv.Players); len(got) != 1 || got[0] != "Dalinar" {
		t.Fatalf("players = %v, want [Dalinar]", got)
	}
	if srv.Players[0].SteamID != "76561190000000001" {
		t.Errorf("SteamID = %q, want the id from the connection", srv.Players[0].SteamID)
	}
}

// Leaving names only the id, so without the binding above nobody would ever
// be removed.
func TestRosterRemovesOnADepartureThatNamesOnlyAnID(t *testing.T) {
	s := apply(withServer("a"), logs("a",
		connect("76561190000000001"),
		join("Dalinar"),
		leave("76561190000000001"),
	))

	srv, _ := s.Server("a")
	if len(srv.Players) != 0 {
		t.Errorf("players = %v, want empty", names(srv.Players))
	}
}

func TestRosterHandlesTwoPlayers(t *testing.T) {
	s := apply(withServer("a"), logs("a",
		connect("id-1"),
		join("Dalinar"),
		connect("id-2"),
		join("Kaladin"),
		leave("id-1"),
	))

	srv, _ := s.Server("a")
	if got := names(srv.Players); len(got) != 1 || got[0] != "Kaladin" {
		t.Errorf("players = %v, want [Kaladin]", got)
	}
}

// A connection that never names itself — the player backed out at character
// select — must not leave a ghost behind for the next join to claim.
func TestAbandonedConnectionIsCleanedUpOnLeave(t *testing.T) {
	s := apply(withServer("a"), logs("a",
		connect("id-ghost"),
		leave("id-ghost"),
		connect("id-real"),
		join("Dalinar"),
	))

	srv, _ := s.Server("a")
	if len(srv.Players) != 1 {
		t.Fatalf("players = %v, want one", names(srv.Players))
	}
	if srv.Players[0].SteamID != "id-real" {
		t.Errorf("SteamID = %q, want the ghost connection not to have been claimed", srv.Players[0].SteamID)
	}
}

// Rejoining must not duplicate a row.
func TestRejoinDoesNotDuplicate(t *testing.T) {
	s := apply(withServer("a"), logs("a",
		connect("id-1"), join("Dalinar"),
		connect("id-1"), join("Dalinar"),
	))

	srv, _ := s.Server("a")
	if got := names(srv.Players); len(got) != 1 {
		t.Errorf("players = %v, want one row", got)
	}
}

// A death is not a departure. Valheim reuses the join line for it, so getting
// this wrong empties the roster every time somebody meets a troll.
func TestDeathDoesNotRemoveThePlayer(t *testing.T) {
	s := apply(withServer("a"), logs("a",
		connect("id-1"),
		join("Dalinar"),
		model.Event{Kind: model.KindDeath, At: at, Player: "Dalinar"},
	))

	srv, _ := s.Server("a")
	if got := names(srv.Players); len(got) != 1 {
		t.Errorf("players = %v, want the player still connected", got)
	}
}

func TestConsoleTailIsBounded(t *testing.T) {
	s := withServer("a")
	for i := 0; i < maxConsole*3; i++ {
		s = apply(s, logs("a", model.Event{Kind: model.KindInfo, At: at, Text: "line"}))
	}

	srv, _ := s.Server("a")
	if got := len(srv.Console); got != maxConsole {
		t.Errorf("console holds %d lines, want the cap %d", got, maxConsole)
	}
}

func TestConsoleKeepsTheNewestLines(t *testing.T) {
	s := withServer("a")
	for i := 0; i < maxConsole+3; i++ {
		s = apply(s, logs("a", model.Event{Kind: model.KindInfo, At: at, Text: itoa(i)}))
	}

	srv, _ := s.Server("a")
	if got := srv.Console[len(srv.Console)-1].Text; got != itoa(maxConsole+2) {
		t.Errorf("newest console line = %q, want the last one written", got)
	}
}

// The fourth dashboard tile: a metric a plugin emits becomes a history with no
// game-specific code between the two.
func TestGameMetricFlowsFromParseToHistory(t *testing.T) {
	s := apply(withServer("a"), logs("a",
		model.Event{Kind: model.KindSave, At: at, Metric: "world_save_ms", Value: 59},
		model.Event{Kind: model.KindSave, At: at.Add(time.Second), Metric: "world_save_ms", Value: 314},
	))

	srv, _ := s.Server("a")
	if got := len(srv.GameMetric.Hot); got != 2 {
		t.Fatalf("metric history has %d points, want 2", got)
	}
	if last, _ := srv.GameMetric.Last(); last.Mean != 314 {
		t.Errorf("newest metric = %v, want 314", last.Mean)
	}
}

func TestMetricLabelComesFromTheObservation(t *testing.T) {
	s := Reduce(Snapshot{}, FleetObserved{
		At:           at,
		Containers:   []host.Container{{Instance: "a", Game: "valheim", State: model.StateRunning}},
		MetricLabels: map[string]string{"valheim": "world save"},
	})

	if got := s.Servers[0].MetricLabel; got != "world save" {
		t.Errorf("MetricLabel = %q, want it taken from the observation", got)
	}
}

// A poll must not empty the console or the roster.
func TestPollKeepsConsoleAndRoster(t *testing.T) {
	s := apply(withServer("a"), logs("a",
		connect("id-1"),
		join("Dalinar"),
		model.Event{Kind: model.KindInfo, At: at, Text: "hello"},
	))
	s = apply(s, observed(host.Container{Instance: "a", State: model.StateRunning}))

	srv, _ := s.Server("a")
	if len(srv.Players) != 1 {
		t.Errorf("the poll emptied the roster")
	}
	if len(srv.Console) == 0 {
		t.Errorf("the poll emptied the console")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func instance(name, game string) model.Instance {
	return model.Instance{Name: name, Game: game, Settings: map[string]any{"ServerName": name}}
}

// A configured server that has never been created is a real thing to show. It
// is the difference between "stopped" and "not built yet", and a fleet that
// hides it gives you nowhere to press the key that creates it.
func TestConfiguredButNotCreatedAppears(t *testing.T) {
	s := apply(Snapshot{},
		InstancesLoaded{At: at, Instances: []model.Instance{instance("valheim-huldra", "valheim")}},
		observed(),
	)

	srv, ok := s.Server("valheim-huldra")
	if !ok {
		t.Fatal("a configured server with no container is missing from the fleet")
	}
	if srv.Created {
		t.Error("Created = true for a server with no container")
	}
	if !srv.Configured {
		t.Error("Configured = false for a server that has a file")
	}
	if srv.Detail == "" {
		t.Error("nothing explains why it is not running")
	}
}

// A container found by label with no file is one Garrison recovered, or one
// somebody made by hand. Hiding it is worse than showing it without settings.
func TestUnconfiguredContainerStillAppears(t *testing.T) {
	s := apply(Snapshot{}, observed(host.Container{
		Instance: "made-by-hand", Game: "valheim", State: model.StateRunning,
	}))

	srv, ok := s.Server("made-by-hand")
	if !ok {
		t.Fatal("a container with no config file vanished from the fleet")
	}
	if srv.Configured {
		t.Error("Configured = true for a server with no file")
	}
	if !srv.Created {
		t.Error("Created = false for a container that exists")
	}
}

func TestConfigAndContainerAreMatchedUp(t *testing.T) {
	s := apply(Snapshot{},
		InstancesLoaded{At: at, Instances: []model.Instance{instance("valheim-huldra", "valheim")}},
		observed(host.Container{Instance: "valheim-huldra", Game: "valheim", State: model.StateRunning}),
	)

	srv, _ := s.Server("valheim-huldra")
	if !srv.Configured || !srv.Created {
		t.Fatalf("Configured=%v Created=%v, want both", srv.Configured, srv.Created)
	}
	if srv.Instance.Settings["ServerName"] != "valheim-huldra" {
		t.Errorf("the settings did not reach the server row: %+v", srv.Instance.Settings)
	}
	if len(s.Servers) != 1 {
		t.Errorf("got %d servers, want the two sources merged into one", len(s.Servers))
	}
}

// The directory is the authority: a file somebody deleted takes its server
// with it, unless a container is still running under that name.
func TestDeletedConfigRemovesTheRow(t *testing.T) {
	s := apply(Snapshot{},
		InstancesLoaded{At: at, Instances: []model.Instance{instance("gone", "valheim")}},
		observed(),
	)
	if _, ok := s.Server("gone"); !ok {
		t.Fatal("setup failed")
	}

	s = apply(s, InstancesLoaded{At: at, Instances: nil})
	if _, ok := s.Server("gone"); ok {
		t.Error("the row survived its config file being deleted")
	}
}

func TestDeletedConfigKeepsARunningContainer(t *testing.T) {
	s := apply(Snapshot{},
		InstancesLoaded{At: at, Instances: []model.Instance{instance("busy", "valheim")}},
		observed(host.Container{Instance: "busy", Game: "valheim", State: model.StateRunning}),
		InstancesLoaded{At: at, Instances: nil},
	)

	srv, ok := s.Server("busy")
	if !ok {
		t.Fatal("a running container vanished when its config file was deleted")
	}
	if srv.Configured {
		t.Error("Configured = true after the file was deleted")
	}
}

// Loading config must not throw away live state, the same way a poll must not.
func TestLoadingConfigKeepsHistoryAndRoster(t *testing.T) {
	s := apply(Snapshot{},
		observed(host.Container{Instance: "a", Game: "valheim", State: model.StateRunning}),
		StatsSampled{At: at, Server: "a", Sample: host.Sample{CPUPct: 5}},
		logs("a", connect("id-1"), join("Dalinar")),
	)

	s = apply(s, InstancesLoaded{At: at, Instances: []model.Instance{instance("a", "valheim")}})

	srv, _ := s.Server("a")
	if len(srv.CPU.Hot) == 0 {
		t.Error("loading config discarded the sampled history")
	}
	if len(srv.Players) != 1 {
		t.Error("loading config discarded the roster")
	}
}
