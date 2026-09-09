package tui_test

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tui"
	"github.com/camden-brown/garrison/internal/tui/comp"
)

var update = flag.Bool("update", false, "rewrite the .golden files")

func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii)
	os.Exit(m.Run())
}

var now = time.Date(2026, 9, 9, 21, 7, 0, 0, time.UTC)

// stubStore stands in for internal/core so the shell can be driven without a
// writer goroutine.
type stubStore struct {
	snap      core.Snapshot
	ch        chan core.Snapshot
	started   []string
	stopped   []string
	edits     []string
	discarded []string
	cancelled []string
}

func newStub(snap core.Snapshot) *stubStore {
	return &stubStore{snap: snap, ch: make(chan core.Snapshot, 1)}
}

func (s *stubStore) Subscribe() <-chan core.Snapshot { return s.ch }
func (s *stubStore) Snapshot() core.Snapshot         { return s.snap }
func (s *stubStore) Start(_ context.Context, instance string) {
	s.started = append(s.started, instance)
}
func (s *stubStore) Stop(_ context.Context, instance string) { s.stopped = append(s.stopped, instance) }

func (s *stubStore) EditSetting(_ context.Context, instance, key string, _ any) {
	s.edits = append(s.edits, instance+"."+key)
}

func (s *stubStore) DiscardDraft(_ context.Context, instance string) {
	s.discarded = append(s.discarded, instance)
}

func (s *stubStore) CancelTask(_ context.Context, id string) {
	s.cancelled = append(s.cancelled, id)
}

// stubView records what it was handed and emits whatever it is told to.
type stubView struct {
	rendered tui.Frame
	emit     tea.Cmd
}

func (v *stubView) ID() tui.ViewID                          { return tui.ViewFleet }
func (v *stubView) Title() string                           { return "Stub" }
func (v *stubView) Keys() []key.Binding                     { return nil }
func (v *stubView) Available(model.Instance) (bool, string) { return true, "" }

func (v *stubView) Update(msg tea.Msg, _ tui.Frame, _ core.Snapshot) (tui.View, tea.Cmd) {
	return v, v.emit
}

func (v *stubView) Render(f tui.Frame, _ core.Snapshot) string {
	v.rendered = f
	return "STAGE"
}

func snapshot() core.Snapshot {
	return core.Reduce(
		core.Snapshot{Engine: core.Engine{Transport: "npipe"}},
		core.FleetObserved{At: now, Containers: []host.Container{
			{Instance: "a", State: model.StateRunning},
			{Instance: "b", State: model.StateStopped},
			{Instance: "c", State: model.StateCrashed},
		}},
	)
}

func newApp(t *testing.T, store tui.Store, v tui.View) *tui.App {
	t.Helper()
	app := tui.NewApp(context.Background(), store, comp.NewTheme(false), v)
	app.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return app
}

// The status bar never changes position, so what it says has to be right
// without being read closely. It names the screen you are on, so the one
// fixed thing on the display also answers "where am I".
func TestStatusBarSummarisesTheFleet(t *testing.T) {
	app := newApp(t, newStub(snapshot()), &stubView{})

	out := app.View()
	for _, want := range []string{"STUB", "3 servers", "1 up", "2 down", "live"} {
		if !strings.Contains(out, want) {
			t.Errorf("status bar is missing %q:\n%s", want, out)
		}
	}
}

func TestStatusBarSaysUnreachableRatherThanNothing(t *testing.T) {
	snap := core.Reduce(snapshot(), core.FleetUnobservable{At: now})
	app := newApp(t, newStub(snap), &stubView{})

	out := app.View()
	if !strings.Contains(out, "stale") {
		t.Errorf("status bar does not report the engine as stale:\n%s", out)
	}
	if !strings.Contains(out, "3 unknown") {
		t.Errorf("status bar does not report the fleet as unknown:\n%s", out)
	}
}

// The stage gets the terminal minus the rail and the status bar. A view told
// it has the whole width draws under the rail; told it has the whole height,
// it pushes the bar off the bottom.
func TestTheStageIsToldItsRealSize(t *testing.T) {
	v := &stubView{}
	newApp(t, newStub(snapshot()), v).View()

	if want := 120 - comp.RailWidth - 2; v.rendered.Width != want {
		t.Errorf("frame width = %d, want %d — the terminal minus the rail", v.rendered.Width, want)
	}
	if v.rendered.Height >= 34 {
		t.Errorf("frame height = %d, want less than the terminal's 34", v.rendered.Height)
	}
}

// Under about 100 columns the rail costs more than it gives, so it goes and
// the stage gets everything.
func TestTheRailIsDroppedWhenTheTerminalIsNarrow(t *testing.T) {
	v := &stubView{}
	app := tui.NewApp(context.Background(), newStub(snapshot()), comp.NewTheme(false), v)
	app.Update(tea.WindowSizeMsg{Width: 70, Height: 24})
	app.View()

	if v.rendered.Width != 70 {
		t.Errorf("frame width = %d, want the whole 70", v.rendered.Width)
	}
}

// Tab cycles three stops in a fixed order. Which one has focus is shown by
// the rail rather than spelled out in the status bar, which the design keeps
// for facts about the fleet.
func TestTabCyclesFocus(t *testing.T) {
	v := &stubView{}
	app := newApp(t, newStub(snapshot()), v)

	// Starts on the stage.
	app.View()
	if !v.rendered.Focused {
		t.Fatal("the stage does not start focused")
	}

	app.Update(tea.KeyMsg{Type: tea.KeyTab}) // servers
	app.View()
	if v.rendered.Focused {
		t.Error("the stage still claims focus after tabbing to the servers")
	}

	app.Update(tea.KeyMsg{Type: tea.KeyTab}) // views
	app.Update(tea.KeyMsg{Type: tea.KeyTab}) // back to the stage
	app.View()
	if !v.rendered.Focused {
		t.Error("three tabs did not return focus to the stage")
	}
}

// Selection belongs to the shell, so moving in the rail changes what a
// per-server view is about.
func TestRailSelectionDrivesTheStage(t *testing.T) {
	v := &stubView{}
	app := newApp(t, newStub(snapshot()), v)

	app.Update(tea.KeyMsg{Type: tea.KeyTab})                       // focus servers
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}) // first server
	app.View()

	if v.rendered.Server != "a" {
		t.Errorf("stage was told server %q, want the rail's selection", v.rendered.Server)
	}
}

// A view asking for a different server must move the rail too, because they
// are one selection seen twice.
func TestSelectMsgMovesTheRail(t *testing.T) {
	v := &stubView{}
	app := newApp(t, newStub(snapshot()), v)

	app.Update(tui.SelectMsg{Server: "c"})
	app.View()

	if v.rendered.Server != "c" {
		t.Errorf("stage was told server %q, want c", v.rendered.Server)
	}
}

// The fleet changes every five seconds. A rail pointing at a container that
// has gone would send every subsequent action nowhere.
func TestSelectionIsDroppedWhenTheServerVanishes(t *testing.T) {
	v := &stubView{}
	store := newStub(snapshot())
	app := newApp(t, store, v)

	// Drive the real subscription rather than fabricating the shell's own
	// message type, so this exercises the path a live store takes.
	cmd := app.Init()
	app.Update(tui.SelectMsg{Server: "c"})

	store.ch <- core.Reduce(core.Snapshot{Engine: core.Engine{OK: true}},
		core.FleetObserved{At: now, Containers: []host.Container{
			{Instance: "a", State: model.StateRunning},
		}})
	app.Update(cmd())
	app.View()

	if v.rendered.Server != "" {
		t.Errorf("stage was told server %q, want the stale selection dropped", v.rendered.Server)
	}
}

// Views never hold the store. They emit an ActionMsg and the shell is the only
// thing that turns one into work.
func TestTheShellDispatchesViewActions(t *testing.T) {
	store := newStub(snapshot())
	app := newApp(t, store, &stubView{emit: tui.Action(core.OpStop, "b")})

	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if cmd == nil {
		t.Fatal("the view's command was dropped")
	}
	app.Update(cmd())

	if len(store.stopped) != 1 || store.stopped[0] != "b" {
		t.Errorf("stopped = %v, want [b]", store.stopped)
	}
	if len(store.started) != 0 {
		t.Errorf("started = %v, want nothing", store.started)
	}
}

// Adding a screen must not mean editing the shell. The registry decides what
// exists; the shell only knows how to switch between them.
func TestViewSwitching(t *testing.T) {
	first := &namedView{id: tui.ViewFleet, title: "Fleet"}
	second := &namedView{id: tui.ViewDashboard, title: "Dashboard"}
	app := tui.NewApp(context.Background(), newStub(snapshot()), comp.NewTheme(false), first, second)
	app.Update(tea.WindowSizeMsg{Width: 120, Height: 34})

	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if got := app.View(); !strings.Contains(got, "DASHBOARD") {
		t.Errorf("pressing 1 did not switch to the dashboard:\n%s", got)
	}

	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if got := app.View(); !strings.Contains(got, "FLEET") {
		t.Errorf("pressing f did not return to the fleet:\n%s", got)
	}
}

// A key naming a view that is not registered must be ignored, not fatal: the
// keymap and the registry are edited separately and will drift.
func TestSwitchingToAnUnregisteredViewIsIgnored(t *testing.T) {
	only := &namedView{id: tui.ViewFleet, title: "Fleet"}
	app := tui.NewApp(context.Background(), newStub(snapshot()), comp.NewTheme(false), only)
	app.Update(tea.WindowSizeMsg{Width: 120, Height: 34})

	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	if got := app.View(); !strings.Contains(got, "FLEET") {
		t.Errorf("the shell moved somewhere that does not exist:\n%s", got)
	}
}

// namedView is a stub that reports an id and title, for switching tests.
type namedView struct {
	stubView
	id    tui.ViewID
	title string
}

func (v *namedView) ID() tui.ViewID { return v.id }
func (v *namedView) Title() string  { return v.title }
func (v *namedView) Update(msg tea.Msg, f tui.Frame, s core.Snapshot) (tui.View, tea.Cmd) {
	return v, nil
}

func TestQuitKeys(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		t.Run(k, func(t *testing.T) {
			app := newApp(t, newStub(snapshot()), &stubView{})

			var msg tea.KeyMsg
			if k == "ctrl+c" {
				msg = tea.KeyMsg{Type: tea.KeyCtrlC}
			} else {
				msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
			}

			_, cmd := app.Update(msg)
			if cmd == nil {
				t.Fatal("no command")
			}
			if _, ok := cmd().(tea.QuitMsg); !ok {
				t.Errorf("%s did not quit", k)
			}
		})
	}
}

// When the store shuts down the subscription closes. Spinning on a closed
// channel would peg a core; the shell stops instead.
func TestShellQuitsWhenTheStoreShutsDown(t *testing.T) {
	app := newApp(t, newStub(snapshot()), &stubView{})
	app.Init()

	store := newStub(snapshot())
	close(store.ch)

	app2 := tui.NewApp(context.Background(), store, comp.NewTheme(false), &stubView{})
	cmd := app2.Init()
	_, quit := app2.Update(cmd())
	if quit == nil {
		t.Fatal("no command after the subscription closed")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Error("the shell did not stop when its store went away")
	}
}

// The whole shell in one render: rail, stage and status bar together. The
// pieces have their own tests; this is the one that catches them not fitting
// beside each other.
func TestShellGolden(t *testing.T) {
	views := []tui.View{
		&namedView{id: tui.ViewFleet, title: "Fleet"},
		&namedView{id: tui.ViewDashboard, title: "Dashboard"},
		&namedView{id: tui.ViewConsole, title: "Console"},
	}

	app := tui.NewApp(context.Background(), newStub(populated()), comp.NewTheme(false), views...)
	app.Now = func() time.Time { return now }
	app.Update(tea.WindowSizeMsg{Width: 120, Height: 20})
	app.Update(tui.SelectMsg{Server: "valheim-huldra"})

	got := app.View()
	golden := filepath.Join("testdata", "shell.golden")

	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatalf("writing golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("reading golden (run with -update to create): %v", err)
	}
	if got != string(want) {
		t.Errorf("shell render differs from %s\n--- got ---\n%s\n--- want ---\n%s", golden, got, want)
	}
}

// Nothing may overflow the terminal. The rail and the stage are joined
// horizontally, so a stage one column too wide pushes the whole thing over.
func TestShellNeverExceedsTheTerminal(t *testing.T) {
	for _, width := range []int{80, 100, 120, 160} {
		app := tui.NewApp(context.Background(), newStub(populated()), comp.NewTheme(false),
			&namedView{id: tui.ViewFleet, title: "Fleet"})
		app.Now = func() time.Time { return now }
		app.Update(tea.WindowSizeMsg{Width: width, Height: 24})

		for i, line := range strings.Split(app.View(), "\n") {
			if w := lipgloss.Width(line); w > width {
				t.Errorf("width %d: line %d is %d cells:\n%q", width, i, w, line)
			}
		}
	}
}

func populated() core.Snapshot {
	s := core.Reduce(
		core.Snapshot{Engine: core.Engine{Transport: "npipe"}},
		core.FleetObserved{At: now, Containers: []host.Container{
			{Instance: "valheim-huldra", Game: "valheim", State: model.StateRunning, Started: now.Add(-59 * time.Hour)},
			{Instance: "zomboid-main", Game: "zomboid", State: model.StateRunning, Started: now.Add(-3 * time.Hour)},
			{Instance: "palworld-sat", Game: "palworld", State: model.StateCrashed, Detail: "OOM killed"},
		}},
	)
	return core.Reduce(s, core.LogEventsRead{At: now, Server: "valheim-huldra", Events: []model.Event{
		{Kind: model.KindConnect, At: now, SteamID: "76561190000000001"},
		{Kind: model.KindJoin, At: now, Player: "Dalinar", Text: "Dalinar joined"},
	}})
}

// Every golden in this repository pins the colour profile to Ascii, which
// emits no escape sequences at all — so a whole class of bug is invisible to
// the rest of the suite. This renders the real shell with colour on and checks
// the geometry survives it.
func TestShellGeometrySurvivesColour(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(termenv.Ascii)

	for _, width := range []int{80, 100, 120, 160} {
		app := tui.NewApp(context.Background(), newStub(populated()), comp.NewTheme(false),
			&namedView{id: tui.ViewFleet, title: "Fleet"})
		app.Now = func() time.Time { return now }
		app.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		app.Update(tui.SelectMsg{Server: "valheim-huldra"})

		out := app.View()
		for i, line := range strings.Split(out, "\n") {
			if w := comp.Width(line); w > width {
				t.Errorf("width %d: line %d is %d visible cells", width, i, w)
			}
			// A "[" that is not preceded by an escape byte is the tail of a
			// sequence that got cut in half, and it prints as literal text
			// beside whatever it was colouring.
			for j := 0; j < len(line); j++ {
				if line[j] == '[' && (j == 0 || line[j-1] != 0x1b) {
					t.Errorf("width %d: line %d has a bare '[' at %d: %q", width, i, j, line)
					break
				}
			}
		}
	}
}
