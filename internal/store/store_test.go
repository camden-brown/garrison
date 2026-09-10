package store_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/store"
	"github.com/camden-brown/garrison/internal/tasks"
)

var at = time.Date(2026, 9, 9, 21, 0, 0, 0, time.UTC)

func open(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "garrison.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func progress(id string, state tasks.State, cursor int) tasks.Progress {
	return tasks.Progress{
		ID: id, Server: "zomboid-main", Kind: tasks.KindUpdate, Trigger: tasks.TriggerScheduled,
		State: state, Cursor: cursor,
		Steps:   []string{"warn", "save", "stop", "snapshot", "update image", "recreate"},
		History: []string{"warned at 15m", "warned at 5m"},
		Queued:  at, Started: at.Add(time.Second),
	}
}

// The database is created on first use, including its directory. A first run
// should not need anything set up by hand.
func TestOpenCreatesEverything(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "garrison.db")

	db, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer db.Close()

	if v, err := db.Version(); err != nil || v == 0 {
		t.Errorf("Version() = %d, %v — migrations did not run", v, err)
	}
}

// Opening twice must be a no-op the second time, which is what makes an
// upgrade that adds a migration safe to ship.
func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garrison.db")

	first, err := store.Open(path)
	if err != nil {
		t.Fatalf("first Open(): %v", err)
	}
	v1, _ := first.Version()
	first.Close()

	second, err := store.Open(path)
	if err != nil {
		t.Fatalf("second Open(): %v", err)
	}
	defer second.Close()

	v2, _ := second.Version()
	if v1 != v2 {
		t.Errorf("version moved from %d to %d on reopen", v1, v2)
	}
}

// Refusing to touch a newer database is the only safe answer. Guessing at a
// schema this build does not know would corrupt it.
func TestARefusalToDowngrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garrison.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Pretend a later Garrison has been here.
	future, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetVersionForTest(future, 999); err != nil {
		t.Fatal(err)
	}
	future.Close()

	if _, err := store.Open(path); err == nil {
		t.Fatal("Open() succeeded against a newer schema")
	} else if !strings.Contains(err.Error(), "newer") {
		t.Errorf("error = %v, want it to say the database is newer", err)
	}
}

func TestJournalRoundTrip(t *testing.T) {
	j := open(t).Journal()

	want := progress("t1", tasks.StateRunning, 3)
	if err := j.Record(want); err != nil {
		t.Fatalf("Record() error = %v", err)
	}

	got, err := j.Recent("", 10)
	if err != nil {
		t.Fatalf("Recent() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d tasks, want 1", len(got))
	}

	p := got[0]
	if p.ID != want.ID || p.Server != want.Server || p.Kind != want.Kind {
		t.Errorf("identity changed: %+v", p)
	}
	if p.Trigger != tasks.TriggerScheduled {
		t.Errorf("Trigger = %v, want scheduled", p.Trigger)
	}
	if p.Cursor != 3 || len(p.Steps) != 6 || p.StepName() != "snapshot" {
		t.Errorf("position lost: cursor=%d steps=%v", p.Cursor, p.Steps)
	}
	if len(p.History) != 2 {
		t.Errorf("history = %v", p.History)
	}
	if !p.Started.Equal(want.Started) {
		t.Errorf("Started = %v, want %v", p.Started, want.Started)
	}
}

// The engine records before every step, so the same id arrives many times. It
// must be one row that moves, not a row per step.
func TestRecordingRepeatedlyUpdatesOneRow(t *testing.T) {
	j := open(t).Journal()

	for i := 0; i < 6; i++ {
		if err := j.Record(progress("t1", tasks.StateRunning, i)); err != nil {
			t.Fatalf("Record() error = %v", err)
		}
	}
	done := progress("t1", tasks.StateDone, 6)
	done.Ended = at.Add(time.Minute)
	if err := j.Record(done); err != nil {
		t.Fatal(err)
	}

	got, _ := j.Recent("", 10)
	if len(got) != 1 {
		t.Fatalf("got %d rows for one task, want 1", len(got))
	}
	if got[0].State != tasks.StateDone || got[0].Cursor != 6 {
		t.Errorf("row did not follow the task: %+v", got[0])
	}
}

// The question this file exists to answer: what was running when the power
// went out.
func TestInterruptedFindsWhatWasRunning(t *testing.T) {
	j := open(t).Journal()

	if err := j.Record(progress("running", tasks.StateRunning, 4)); err != nil {
		t.Fatal(err)
	}
	finished := progress("finished", tasks.StateDone, 6)
	finished.Ended = at.Add(time.Minute)
	if err := j.Record(finished); err != nil {
		t.Fatal(err)
	}
	if err := j.Record(progress("queued", tasks.StateQueued, 0)); err != nil {
		t.Fatal(err)
	}

	got, err := j.Interrupted()
	if err != nil {
		t.Fatalf("Interrupted() error = %v", err)
	}

	found := map[string]bool{}
	for _, p := range got {
		found[p.ID] = true
	}
	if !found["running"] {
		t.Error("a task left running was not reported as interrupted")
	}
	if !found["queued"] {
		t.Error("a task left queued was not reported — it never ran, but nothing will run it now either")
	}
	if found["finished"] {
		t.Error("a finished task was reported as interrupted")
	}
}

// An interrupted task must come back with enough detail to say where it
// stopped, because that is the only thing telling the operator what to check.
func TestInterruptedKeepsItsPosition(t *testing.T) {
	j := open(t).Journal()
	if err := j.Record(progress("t1", tasks.StateRunning, 4)); err != nil {
		t.Fatal(err)
	}

	got, _ := j.Interrupted()
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	if got[0].Cursor != 4 || got[0].StepName() != "update image" {
		t.Errorf("position = %d (%s), want step 5 'update image'", got[0].Cursor, got[0].StepName())
	}
}

func TestRecentFiltersByServer(t *testing.T) {
	j := open(t).Journal()

	a := progress("a1", tasks.StateDone, 6)
	a.Server = "alpha"
	b := progress("b1", tasks.StateDone, 6)
	b.Server = "beta"
	if err := j.Record(a); err != nil {
		t.Fatal(err)
	}
	if err := j.Record(b); err != nil {
		t.Fatal(err)
	}

	got, _ := j.Recent("alpha", 10)
	if len(got) != 1 || got[0].Server != "alpha" {
		t.Errorf("Recent(alpha) = %+v", got)
	}
}

// Everything here is bounded. A journal that only grows is a slow leak with a
// paper trail.
func TestPruneDropsOldFinishedTasksOnly(t *testing.T) {
	j := open(t).Journal()

	old := progress("old", tasks.StateDone, 6)
	old.Ended = at.Add(-90 * 24 * time.Hour)
	recent := progress("recent", tasks.StateDone, 6)
	recent.Ended = at
	live := progress("live", tasks.StateRunning, 2)

	for _, p := range []tasks.Progress{old, recent, live} {
		if err := j.Record(p); err != nil {
			t.Fatal(err)
		}
	}

	n, err := j.Prune(at.Add(-30 * 24 * time.Hour))
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d, want 1", n)
	}

	got, _ := j.Recent("", 10)
	left := map[string]bool{}
	for _, p := range got {
		left[p.ID] = true
	}
	if left["old"] {
		t.Error("the old task survived")
	}
	if !left["recent"] || !left["live"] {
		t.Errorf("pruning took too much: %v", left)
	}
}

// States are stored as words so a person reading the file with sqlite3 can
// tell what happened, and so renumbering a Go enum cannot reinterpret history.
func TestStatesRoundTripThroughTheirNames(t *testing.T) {
	j := open(t).Journal()

	for _, state := range []tasks.State{
		tasks.StateDone, tasks.StateFailed, tasks.StateCanceled, tasks.StateRolledBack,
	} {
		p := progress("t-"+state.String(), state, 6)
		p.Ended = at
		if err := j.Record(p); err != nil {
			t.Fatal(err)
		}
	}

	got, _ := j.Recent("", 10)
	seen := map[tasks.State]bool{}
	for _, p := range got {
		seen[p.State] = true
	}
	for _, want := range []tasks.State{
		tasks.StateDone, tasks.StateFailed, tasks.StateCanceled, tasks.StateRolledBack,
	} {
		if !seen[want] {
			t.Errorf("state %v did not survive the round trip", want)
		}
	}
}

func TestMetricsRoundTrip(t *testing.T) {
	db := open(t)

	for i := 0; i < 5; i++ {
		p := model.Point{At: at.Add(time.Duration(i) * time.Minute), Mean: float64(i), Min: 0, Max: float64(i) * 2}
		if err := db.RecordMetric("a", store.SeriesCPU, p); err != nil {
			t.Fatalf("RecordMetric() error = %v", err)
		}
	}

	got, err := db.Metrics("a", store.SeriesCPU, at)
	if err != nil {
		t.Fatalf("Metrics() error = %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d points, want 5", len(got))
	}
	// Min and max survive, which is the reason they are carried at all: a
	// spike averaged away is the thing you went looking for.
	if got[4].Max != 8 {
		t.Errorf("Max = %v, want 8", got[4].Max)
	}
	if !got[0].At.Equal(at) {
		t.Errorf("At = %v, want %v", got[0].At, at)
	}
}

// Only what somebody will ask about later. Writing every INFO line would grow
// gigabytes a week and answer nothing.
func TestOnlyKeepableEventsAreStored(t *testing.T) {
	db := open(t)

	events := []model.Event{
		{Kind: model.KindJoin, At: at, Player: "Dalinar"},
		{Kind: model.KindInfo, At: at, Text: "Zonesystem Start"},
		{Kind: model.KindSave, At: at, Text: "world saved"},
		{Kind: model.KindDeath, At: at, Player: "Dalinar"},
		{Kind: model.KindChat, At: at, Player: "Dalinar", Text: "hello"},
	}
	for _, ev := range events {
		if err := db.RecordEvent("a", ev); err != nil {
			t.Fatalf("RecordEvent() error = %v", err)
		}
	}

	got, err := db.Events("a", at.Add(-time.Hour), 100)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("stored %d events, want the 3 worth keeping", len(got))
	}
	for _, ev := range got {
		if !store.Keepable(ev.Kind) {
			t.Errorf("stored an event of kind %v", ev.Kind)
		}
	}
}

func TestEventKindSurvivesTheRoundTrip(t *testing.T) {
	db := open(t)
	if err := db.RecordEvent("a", model.Event{Kind: model.KindDeath, At: at, Player: "Odger"}); err != nil {
		t.Fatal(err)
	}

	got, _ := db.Events("a", at.Add(-time.Hour), 10)
	if len(got) != 1 || got[0].Kind != model.KindDeath || got[0].Player != "Odger" {
		t.Errorf("round trip = %+v", got)
	}
}

func TestPruning(t *testing.T) {
	db := open(t)

	old := at.Add(-100 * 24 * time.Hour)
	if err := db.RecordMetric("a", store.SeriesCPU, model.Point{At: old, Mean: 1}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordMetric("a", store.SeriesCPU, model.Point{At: at, Mean: 2}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordEvent("a", model.Event{Kind: model.KindJoin, At: old, Player: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordEvent("a", model.Event{Kind: model.KindJoin, At: at, Player: "y"}); err != nil {
		t.Fatal(err)
	}

	if n, err := db.PruneMetrics(at.Add(-30 * 24 * time.Hour)); err != nil || n != 1 {
		t.Errorf("PruneMetrics() = %d, %v, want 1", n, err)
	}
	if n, err := db.PruneEvents(at.Add(-90 * 24 * time.Hour)); err != nil || n != 1 {
		t.Errorf("PruneEvents() = %d, %v, want 1", n, err)
	}

	points, _ := db.Metrics("a", store.SeriesCPU, old.Add(-time.Hour))
	if len(points) != 1 {
		t.Errorf("%d metric points left, want 1", len(points))
	}
}

func TestSessionsRoundTrip(t *testing.T) {
	db := open(t)

	joined := time.Date(2026, 9, 9, 20, 0, 0, 0, time.UTC)
	id, err := db.SessionOpened("a", model.Player{Name: "Huldra", SteamID: "76561"}, joined)
	if err != nil {
		t.Fatalf("SessionOpened: %v", err)
	}

	// Still on: the session comes back open, which is what separates
	// "played for two hours" from "has been on for two hours".
	got, err := db.SessionsSince("a", joined.Add(-time.Hour))
	if err != nil {
		t.Fatalf("SessionsSince: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d sessions, want 1", len(got))
	}
	if !got[0].Open() {
		t.Error("a session with nobody having left is not open")
	}
	if got[0].Player != "Huldra" || got[0].SteamID != "76561" {
		t.Errorf("session = %+v, want the player it was opened for", got[0])
	}

	if err := db.SessionClosed(id, joined.Add(2*time.Hour)); err != nil {
		t.Fatalf("SessionClosed: %v", err)
	}
	got, _ = db.SessionsSince("a", joined.Add(-time.Hour))
	if got[0].Open() {
		t.Error("the session is still open after being closed")
	}
	if d := got[0].Duration(joined.Add(5 * time.Hour)); d != 2*time.Hour {
		t.Errorf("duration = %v, want 2h — a closed session does not keep growing", d)
	}
}

func TestSessionsAreScopedToTheirServer(t *testing.T) {
	db := open(t)
	at := time.Date(2026, 9, 9, 20, 0, 0, 0, time.UTC)

	db.SessionOpened("a", model.Player{Name: "Huldra"}, at)
	db.SessionOpened("b", model.Player{Name: "Bjorn"}, at)

	got, _ := db.SessionsSince("a", at.Add(-time.Hour))
	if len(got) != 1 || got[0].Player != "Huldra" {
		t.Errorf("server a has %v, want only its own session", got)
	}
}

func TestSessionsRespectTheWindow(t *testing.T) {
	db := open(t)
	old := time.Date(2026, 9, 1, 20, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 9, 9, 20, 0, 0, 0, time.UTC)

	db.SessionOpened("a", model.Player{Name: "Old"}, old)
	db.SessionOpened("a", model.Player{Name: "Recent"}, recent)

	got, _ := db.SessionsSince("a", recent.Add(-24*time.Hour))
	if len(got) != 1 || got[0].Player != "Recent" {
		t.Errorf("got %v, want only the session inside the window", got)
	}
}

// A session left open by a Garrison that was killed is closed at the time it
// was last seen, not at the time of the next start — otherwise three days of
// downtime becomes a three-day session at the top of every chart.
func TestCloseStaleSessionsDoesNotInventPlaytime(t *testing.T) {
	db := open(t)
	joined := time.Date(2026, 9, 9, 20, 0, 0, 0, time.UTC)

	db.SessionOpened("a", model.Player{Name: "Huldra"}, joined)

	closed, err := db.CloseStaleSessions(joined.Add(72 * time.Hour))
	if err != nil {
		t.Fatalf("CloseStaleSessions: %v", err)
	}
	if closed != 1 {
		t.Errorf("closed %d sessions, want 1", closed)
	}

	got, _ := db.SessionsSince("a", joined.Add(-time.Hour))
	if got[0].Open() {
		t.Fatal("the stale session is still open")
	}
	if d := got[0].Duration(joined.Add(72 * time.Hour)); d != 0 {
		t.Errorf("stale session lasted %v, want 0 — downtime is not playtime", d)
	}
}

func TestPruneSessions(t *testing.T) {
	db := open(t)
	old := time.Date(2026, 9, 1, 20, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 9, 9, 20, 0, 0, 0, time.UTC)

	db.SessionOpened("a", model.Player{Name: "Old"}, old)
	db.SessionOpened("a", model.Player{Name: "Recent"}, recent)

	n, err := db.PruneSessions(recent.Add(-24 * time.Hour))
	if err != nil {
		t.Fatalf("PruneSessions: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d, want 1", n)
	}
	got, _ := db.SessionsSince("a", time.Time{})
	if len(got) != 1 {
		t.Errorf("got %d sessions after pruning, want 1", len(got))
	}
}
