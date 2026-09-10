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

	// The wizard offers the registered games, so the shell's tests need
	// them registered — the same blank import cmd uses.
	_ "github.com/camden-brown/garrison/internal/games/all"
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
	restarted []string
	backedUp  []string
	restored  []string
	notices   []string
	created   []model.Instance
	deleted   []string
	commands  []string
	updated   []string
	applied   []string
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

func (s *stubStore) Restart(_ context.Context, instance string) {
	s.restarted = append(s.restarted, instance)
}
func (s *stubStore) Backup(_ context.Context, instance string) {
	s.backedUp = append(s.backedUp, instance)
}
func (s *stubStore) CreateServer(_ context.Context, inst model.Instance) {
	s.created = append(s.created, inst)
}
func (s *stubStore) DeleteServer(_ context.Context, instance string) {
	s.deleted = append(s.deleted, instance)
}
func (s *stubStore) SendCommand(_ context.Context, instance, text string) {
	s.commands = append(s.commands, instance+": "+text)
}
func (s *stubStore) Notify(_ context.Context, server, text string) {
	s.notices = append(s.notices, server+": "+text)
}
func (s *stubStore) Restore(_ context.Context, instance, archive string) {
	s.restored = append(s.restored, instance+" <- "+archive)
}
func (s *stubStore) Update(_ context.Context, instance string) {
	s.updated = append(s.updated, instance)
}
func (s *stubStore) ApplySettings(_ context.Context, instance string, recreate bool) {
	s.applied = append(s.applied, instance)
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
			{Instance: "factorio-main", Game: "factorio", State: model.StateCrashed, Detail: "OOM killed"},
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

// typeLine drives the command line the way a person does: one key per rune.
func typeLine(app *tui.App, text string) {
	for _, r := range text {
		if r == ' ' {
			app.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
			continue
		}
		app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func openCommand(app *tui.App) {
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
}

// The command line's reason to exist: naming a server other than the one the
// rail is pointing at, which no key can express.
func TestCommandLineActsOnANamedServer(t *testing.T) {
	store := newStub(snapshot())
	app := newApp(t, store, &stubView{})

	openCommand(app)
	typeLine(app, "stop b")
	if _, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		app.Update(cmd())
	}

	if len(store.stopped) != 1 || store.stopped[0] != "b" {
		t.Errorf("stopped %v, want [b]", store.stopped)
	}
}

// While the line is open every key is text, including q — which otherwise
// quits, and would do it halfway through typing "backup".
func TestCommandLineSwallowsTheShellsKeys(t *testing.T) {
	store := newStub(snapshot())
	app := newApp(t, store, &stubView{})

	openCommand(app)
	typeLine(app, "backup a")

	out := app.View()
	if !strings.Contains(out, "backup a") {
		t.Errorf("the typed line is not on screen:\n%s", out)
	}
	if len(store.started)+len(store.stopped)+len(store.backedUp) != 0 {
		t.Error("typing the line already did something")
	}
}

func TestCommandLineCancels(t *testing.T) {
	store := newStub(snapshot())
	app := newApp(t, store, &stubView{})

	openCommand(app)
	typeLine(app, "stop a")
	app.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if len(store.stopped) != 0 {
		t.Errorf("esc stopped %v anyway", store.stopped)
	}
	if strings.Contains(app.View(), "stop a") {
		t.Error("esc left the line on screen")
	}
}

// A typo has to say so. A command line that silently ignores what it did not
// understand is one you cannot trust with a verb that stops things.
func TestUnknownCommandsAreReported(t *testing.T) {
	store := newStub(snapshot())
	app := newApp(t, store, &stubView{})

	openCommand(app)
	typeLine(app, "detonate a")
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if len(store.notices) == 0 {
		t.Fatal("an unknown command produced no notice")
	}
	if !strings.Contains(store.notices[0], "detonate") {
		t.Errorf("notice = %q, want it to name the command", store.notices[0])
	}
}

func TestUnknownServersAreReported(t *testing.T) {
	store := newStub(snapshot())
	app := newApp(t, store, &stubView{})

	openCommand(app)
	typeLine(app, "stop nowhere")
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if len(store.stopped) != 0 {
		t.Errorf("stopped %v, want nothing", store.stopped)
	}
	if len(store.notices) == 0 || !strings.Contains(store.notices[0], "nowhere") {
		t.Errorf("notices = %v, want one naming the server", store.notices)
	}
}

func TestCommandTabCompletes(t *testing.T) {
	store := newStub(snapshot())
	app := newApp(t, store, &stubView{})

	openCommand(app)
	typeLine(app, "ba")
	app.Update(tea.KeyMsg{Type: tea.KeyTab})

	if out := app.View(); !strings.Contains(out, "backup") {
		t.Errorf("tab did not complete the verb:\n%s", out)
	}
}

// Completion extends to the longest agreement and no further. "st" is both
// start and stop, and guessing between them on a command line that stops
// servers is how the wrong one goes down.
func TestCommandTabDoesNotGuessBetweenVerbs(t *testing.T) {
	store := newStub(snapshot())
	app := newApp(t, store, &stubView{})

	openCommand(app)
	typeLine(app, "st")
	app.Update(tea.KeyMsg{Type: tea.KeyTab})

	out := app.View()
	if strings.Contains(out, "start") || strings.Contains(out, "stop") {
		t.Errorf("tab picked one of start/stop:\n%s", out)
	}
}

// The line takes a row from the stage rather than covering one. Overlaying
// the row you are acting on is how you act on the wrong one.
func TestTheCommandLineCostsTheStageARow(t *testing.T) {
	v := &stubView{}
	app := newApp(t, newStub(snapshot()), v)

	app.View()
	before := v.rendered.Height

	openCommand(app)
	app.View()

	if v.rendered.Height != before-1 {
		t.Errorf("stage height went %d -> %d, want one row given up", before, v.rendered.Height)
	}
}

// Ambient mode drops the chrome. The status bar going is the point rather than
// an oversight — it is the row that makes this look like a tool you are using
// rather than a thing you left on a second monitor.
func TestAmbientDropsTheChrome(t *testing.T) {
	app := newApp(t, newStub(snapshot()), &stubView{})

	normal := app.View()
	if !strings.Contains(normal, "STUB") {
		t.Fatalf("the status bar is missing before ambient mode:\n%s", normal)
	}

	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'F'}})
	got := app.View()

	if strings.Contains(got, "STUB") {
		t.Errorf("ambient mode kept the status bar:\n%s", got)
	}
	for _, want := range []string{"A", "B", "C"} {
		if !strings.Contains(strings.ToUpper(got), want) {
			t.Errorf("ambient mode is missing a card for %q:\n%s", want, got)
		}
	}
}

// Any key leaves. A mode you have to remember the exit key for is a mode
// somebody force-quits the terminal out of.
func TestAnyKeyLeavesAmbient(t *testing.T) {
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'j'}},
		{Type: tea.KeyEsc},
		{Type: tea.KeyRunes, Runes: []rune{'F'}},
	} {
		app := newApp(t, newStub(snapshot()), &stubView{})
		app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'F'}})
		app.Update(k)

		if got := app.View(); !strings.Contains(got, "STUB") {
			t.Errorf("%v did not leave ambient mode:\n%s", k, got)
		}
	}
}

// The one key that always works still always works.
func TestCtrlCQuitsFromAmbient(t *testing.T) {
	app := newApp(t, newStub(snapshot()), &stubView{})
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'F'}})

	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c in ambient mode produced no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("ctrl+c in ambient mode did not quit")
	}
}

func TestAmbientFitsTheTerminal(t *testing.T) {
	for _, width := range []int{80, 92, 120, 160} {
		app := tui.NewApp(context.Background(), newStub(snapshot()), comp.NewTheme(false), &stubView{})
		app.Update(tea.WindowSizeMsg{Width: width, Height: 34})
		app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'F'}})

		for i, line := range strings.Split(app.View(), "\n") {
			if w := comp.Width(line); w > width {
				t.Errorf("width %d: ambient line %d is %d cells", width, i, w)
			}
		}
	}
}

func openPalette(app *tui.App) {
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
}

func TestPaletteOffersServersViewsAndVerbs(t *testing.T) {
	app := newApp(t, newStub(snapshot()), &stubView{})
	openPalette(app)

	got := app.View()
	for _, want := range []string{"PALETTE", "server", "view", "start"} {
		if !strings.Contains(got, want) {
			t.Errorf("the palette is missing %q:\n%s", want, got)
		}
	}
}

// Subsequence matching is what makes the palette worth opening: "vh" should
// reach "valheim-huldra" without typing the whole name.
func TestPaletteMatchesASubsequence(t *testing.T) {
	snap := core.Reduce(
		core.Snapshot{Engine: core.Engine{Transport: "npipe"}},
		core.FleetObserved{At: now, Containers: []host.Container{
			{Instance: "valheim-huldra", State: model.StateRunning},
			{Instance: "zomboid-main", State: model.StateStopped},
		}},
	)
	app := newApp(t, newStub(snap), &stubView{})

	openPalette(app)
	typeLine(app, "vh")

	// The rail lists every server regardless, so the assertion is about the
	// palette's own rows rather than the whole screen.
	got := app.View()
	panel := got[strings.Index(got, "PALETTE"):]

	if !strings.Contains(panel, "valheim-huldra") {
		t.Errorf("the subsequence did not reach the server:\n%s", panel)
	}
	if strings.Contains(panel, "zomboid-main") {
		t.Errorf("a non-matching server survived:\n%s", panel)
	}
}

// Enter on a server row selects it rather than doing something to it. The
// palette navigates as well as acts, and the difference matters when the list
// also contains "stop".
func TestPaletteSelectsAServer(t *testing.T) {
	store := newStub(snapshot())
	app := newApp(t, store, &stubView{})

	openPalette(app)
	typeLine(app, "b")
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if len(store.stopped)+len(store.started) != 0 {
		t.Error("selecting a server in the palette ran a verb")
	}
	if got := app.View(); strings.Contains(got, "PALETTE") {
		t.Errorf("the palette stayed open after enter:\n%s", got)
	}
}

func TestPaletteRunsAVerb(t *testing.T) {
	store := newStub(snapshot())
	app := newApp(t, store, &stubView{})

	// Select a server first so the verb has a target.
	openPalette(app)
	typeLine(app, "b")
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	openPalette(app)
	typeLine(app, "stop")
	if _, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		app.Update(cmd())
	}

	if len(store.stopped) != 1 || store.stopped[0] != "b" {
		t.Errorf("stopped %v, want [b]", store.stopped)
	}
}

func TestPaletteEscapeCloses(t *testing.T) {
	app := newApp(t, newStub(snapshot()), &stubView{})

	openPalette(app)
	app.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if got := app.View(); strings.Contains(got, "PALETTE") {
		t.Errorf("esc did not close the palette:\n%s", got)
	}
}

// While it is open every key is text, or typing "stop" quits on the q that
// is not there and jumps views on the numbers that are.
func TestPaletteSwallowsTheShellsKeys(t *testing.T) {
	app := newApp(t, newStub(snapshot()), &stubView{})

	openPalette(app)
	typeLine(app, "q1")

	if got := app.View(); !strings.Contains(got, "PALETTE") {
		t.Errorf("the palette closed while being typed into:\n%s", got)
	}
}

func TestPaletteFitsTheTerminal(t *testing.T) {
	for _, width := range []int{80, 92, 120} {
		app := tui.NewApp(context.Background(), newStub(snapshot()), comp.NewTheme(false), &stubView{})
		app.Update(tea.WindowSizeMsg{Width: width, Height: 34})
		openPalette(app)
		typeLine(app, "s")

		for i, line := range strings.Split(app.View(), "\n") {
			if w := comp.Width(line); w > width {
				t.Errorf("width %d: palette line %d is %d cells", width, i, w)
			}
		}
	}
}

func openWizard(app *tui.App) {
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
}

func TestWizardCreatesAServer(t *testing.T) {
	store := newStub(snapshot())
	app := newApp(t, store, &stubView{})

	openWizard(app)
	app.Update(tea.KeyMsg{Type: tea.KeyTab}) // game -> name
	typeLine(app, "valheim-new")
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if len(store.created) != 1 {
		t.Fatalf("created %d servers, want 1", len(store.created))
	}
	got := store.created[0]
	if got.Name != "valheim-new" {
		t.Errorf("name = %q, want valheim-new", got.Name)
	}
	if got.Game == "" || got.Image == "" {
		t.Errorf("instance = %+v, want the game's id and default image filled in", got)
	}
	if got.Data == "" {
		t.Error("no data directory was proposed")
	}
}

// The wizard's reason to exist: the second Valheim server does not collide
// with the first. Ports come from the live fleet, not from the game's
// defaults alone.
func TestWizardProposesPortsAroundTheFleet(t *testing.T) {
	snap := core.Reduce(
		core.Snapshot{Engine: core.Engine{Transport: "npipe"}},
		core.FleetObserved{At: now, Containers: []host.Container{
			{Instance: "valheim-one", Game: "valheim", State: model.StateRunning,
				Ports: []model.PortMap{{Container: "2456/udp", Host: 2456}, {Container: "2457/udp", Host: 2457}}},
		}},
	)
	store := newStub(snap)
	app := newApp(t, store, &stubView{})

	openWizard(app)
	app.Update(tea.KeyMsg{Type: tea.KeyTab})
	typeLine(app, "valheim-two")
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if len(store.created) != 1 {
		t.Fatalf("created %d servers, want 1", len(store.created))
	}
	for _, p := range store.created[0].Ports {
		if p.Host == 2456 || p.Host == 2457 {
			t.Errorf("proposed port %d, which the fleet is already using", p.Host)
		}
	}
	if len(store.created[0].Ports) == 0 {
		t.Error("no ports were proposed")
	}
}

// A name that cannot be a file name or a container name is refused here
// rather than surfacing as a Docker error three steps later.
func TestWizardRefusesImpossibleNames(t *testing.T) {
	for _, name := range []string{"", "has space", "slash/es"} {
		store := newStub(snapshot())
		app := newApp(t, store, &stubView{})

		openWizard(app)
		app.Update(tea.KeyMsg{Type: tea.KeyTab})
		typeLine(app, name)
		app.Update(tea.KeyMsg{Type: tea.KeyEnter})

		if len(store.created) != 0 {
			t.Errorf("%q was accepted as a server name", name)
		}
		if len(store.notices) == 0 {
			t.Errorf("%q was refused without saying why", name)
		}
	}
}

func TestWizardRefusesADuplicateName(t *testing.T) {
	store := newStub(snapshot())
	app := newApp(t, store, &stubView{})

	openWizard(app)
	app.Update(tea.KeyMsg{Type: tea.KeyTab})
	typeLine(app, "a") // already in the fixture fleet
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if len(store.created) != 0 {
		t.Error("a duplicate name was accepted")
	}
}

func TestWizardEscapeCancels(t *testing.T) {
	store := newStub(snapshot())
	app := newApp(t, store, &stubView{})

	openWizard(app)
	app.Update(tea.KeyMsg{Type: tea.KeyTab})
	typeLine(app, "valheim-new")
	app.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if len(store.created) != 0 {
		t.Error("esc created a server anyway")
	}
	if got := app.View(); strings.Contains(got, "NEW SERVER") {
		t.Errorf("esc left the wizard open:\n%s", got)
	}
}

// While the form is open every key is form input, or typing a name presses
// the shell's own bindings on the way past.
func TestWizardSwallowsTheShellsKeys(t *testing.T) {
	store := newStub(snapshot())
	app := newApp(t, store, &stubView{})

	openWizard(app)
	app.Update(tea.KeyMsg{Type: tea.KeyTab})
	typeLine(app, "qf1")

	if got := app.View(); !strings.Contains(got, "NEW SERVER") {
		t.Errorf("the wizard closed while being typed into:\n%s", got)
	}
}

func TestWizardFitsTheTerminal(t *testing.T) {
	for _, width := range []int{80, 92, 120} {
		app := tui.NewApp(context.Background(), newStub(snapshot()), comp.NewTheme(false), &stubView{})
		app.Update(tea.WindowSizeMsg{Width: width, Height: 34})
		openWizard(app)
		app.Update(tea.KeyMsg{Type: tea.KeyTab})
		typeLine(app, "valheim-new")

		for i, line := range strings.Split(app.View(), "\n") {
			if w := comp.Width(line); w > width {
				t.Errorf("width %d: wizard line %d is %d cells", width, i, w)
			}
		}
	}
}

// serverWith builds a fleet of one configured server, which is what the share
// panel needs — the address and the settings live on the instance.
func serverWith(inst model.Instance) core.Snapshot {
	return core.Reduce(
		core.Snapshot{Engine: core.Engine{Transport: "npipe"}},
		core.InstancesLoaded{At: now, Instances: []model.Instance{inst}},
		core.FleetObserved{At: now, Containers: []host.Container{
			{Instance: inst.Name, Game: inst.Game, State: model.StateRunning,
				Ports: inst.Ports},
		}},
	)
}

func valheimInstance() model.Instance {
	return model.Instance{
		Name: "valheim-main", Game: "valheim", Data: "/data",
		Address: "shatterplain.duckdns.org",
		Ports:   []model.PortMap{{Container: "2456/udp", Host: 2456}},
		Settings: map[string]any{
			"ServerName": "The Shattered Plains",
			"ServerPass": "gemliowvp18",
			"WorldName":  "Midgard",
		},
	}
}

func shareAndRead(t *testing.T, snap core.Snapshot, server string) (*tui.App, string) {
	t.Helper()
	app := newApp(t, newStub(snap), &stubView{})

	// The rail starts on the fleet; the palette is the shortest way to pick
	// a server without depending on arrow-key geometry.
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	typeLine(app, server)
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})

	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	return app, app.View()
}

// What a player actually needs, in one paste: where to connect, and the
// password to get in.
func TestShareCopiesTheJoinDetails(t *testing.T) {
	_, got := shareAndRead(t, serverWith(valheimInstance()), "valheim-main")

	for _, want := range []string{
		"SHARE",
		"The Shattered Plains",
		"shatterplain.duckdns.org:2456",
		"gemliowvp18",
		"Midgard",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the share panel is missing %q:\n%s", want, got)
		}
	}
}

// The panel is the receipt. OSC 52 is advisory — the terminal may drop it —
// so a feature whose only evidence is somebody else's paste would fail
// silently.
func TestSharePanelShowsWhatWasCopied(t *testing.T) {
	_, got := shareAndRead(t, serverWith(valheimInstance()), "valheim-main")

	if !strings.Contains(got, "OSC 52") {
		t.Errorf("the panel does not offer a fallback for terminals that ignore it:\n%s", got)
	}
}

// Garrison cannot work out a public address, so it says so rather than
// pasting a LAN address that works for nobody outside the house.
func TestShareSaysWhenTheAddressIsNotSet(t *testing.T) {
	inst := valheimInstance()
	inst.Address = ""

	_, got := shareAndRead(t, serverWith(inst), "valheim-main")

	if !strings.Contains(got, "not set") {
		t.Errorf("a server with no address should say so:\n%s", got)
	}
	if strings.Contains(got, ":2456") {
		t.Errorf("a port was pasted with no host to go with it:\n%s", got)
	}
}

func TestShareSaysWhenThereIsNoPassword(t *testing.T) {
	inst := valheimInstance()
	delete(inst.Settings, "ServerPass")

	_, got := shareAndRead(t, serverWith(inst), "valheim-main")

	if !strings.Contains(got, "Password: none") && !strings.Contains(got, "none") {
		t.Errorf("an open server should say the password is none:\n%s", got)
	}
	if strings.Contains(got, "gemliowvp18") {
		t.Errorf("a password appeared for a server that has none:\n%s", got)
	}
}

// The panel is a receipt rather than a mode: the next key clears it and is
// otherwise handled normally.
func TestSharePanelClearsOnTheNextKey(t *testing.T) {
	app, got := shareAndRead(t, serverWith(valheimInstance()), "valheim-main")
	if !strings.Contains(got, "SHARE") {
		t.Fatalf("the panel did not open:\n%s", got)
	}

	app.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if after := app.View(); strings.Contains(after, "SHARE") {
		t.Errorf("the panel outlived the next key:\n%s", after)
	}
}

// Sharing "the fleet" is not a thing, and it should say so rather than
// copying an empty template.
func TestShareWithNoServerSelectedExplains(t *testing.T) {
	store := newStub(serverWith(valheimInstance()))
	app := newApp(t, store, &stubView{})

	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}) // fleet: nothing selected
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	if strings.Contains(app.View(), "SHARE") {
		t.Error("the share panel opened with no server selected")
	}
	if len(store.notices) == 0 {
		t.Error("sharing with no server selected said nothing")
	}
}

func TestSharePanelFitsTheTerminal(t *testing.T) {
	for _, width := range []int{80, 92, 120} {
		app := tui.NewApp(context.Background(), newStub(serverWith(valheimInstance())),
			comp.NewTheme(false), &stubView{})
		app.Update(tea.WindowSizeMsg{Width: width, Height: 34})
		app.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
		typeLine(app, "valheim-main")
		app.Update(tea.KeyMsg{Type: tea.KeyEnter})
		app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

		for i, line := range strings.Split(app.View(), "\n") {
			if w := comp.Width(line); w > width {
				t.Errorf("width %d: share line %d is %d cells", width, i, w)
			}
		}
	}
}

// The status bar has advertised "? help" since M0. This is the test that it
// is not lying.
func TestHelpOpensAndListsTheKeys(t *testing.T) {
	app := newApp(t, newStub(snapshot()), &stubView{})
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})

	got := app.View()
	for _, want := range []string{"HELP", "EVERYWHERE", "palette", "ambient", "join details"} {
		if !strings.Contains(got, want) {
			t.Errorf("help is missing %q:\n%s", want, got)
		}
	}
}

// Generated from the View contract rather than written by hand, which is the
// reason Keys() exists at all.
func TestHelpListsTheActiveViewsKeys(t *testing.T) {
	app := newApp(t, newStub(snapshot()), &stubView{})
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})

	if got := app.View(); !strings.Contains(got, "STUB") {
		t.Errorf("help does not name the active screen:\n%s", got)
	}
}

func TestAnyKeyClosesHelp(t *testing.T) {
	app := newApp(t, newStub(snapshot()), &stubView{})
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	app.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if got := app.View(); strings.Contains(got, "EVERYWHERE") {
		t.Errorf("esc did not close help:\n%s", got)
	}
}

func TestHelpFitsTheTerminal(t *testing.T) {
	for _, width := range []int{80, 92, 120} {
		app := tui.NewApp(context.Background(), newStub(snapshot()), comp.NewTheme(false), &stubView{})
		app.Update(tea.WindowSizeMsg{Width: width, Height: 34})
		app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})

		for i, line := range strings.Split(app.View(), "\n") {
			if w := comp.Width(line); w > width {
				t.Errorf("width %d: help line %d is %d cells", width, i, w)
			}
		}
	}
}

// configured builds a fleet whose servers Garrison has files for, which is
// what delete needs — a container found by label with no configuration has
// nothing of Garrison's own to remove.
func configuredFleet() core.Snapshot {
	insts := []model.Instance{
		{Name: "a", Game: "valheim", Data: `C:\gameservers\a`},
		{Name: "b", Game: "valheim", Data: `C:\gameservers\b`},
	}
	return core.Reduce(
		core.Snapshot{Engine: core.Engine{Transport: "npipe"}},
		core.InstancesLoaded{At: now, Instances: insts},
		core.FleetObserved{At: now, Containers: []host.Container{
			{Instance: "a", Game: "valheim", State: model.StateRunning},
			{Instance: "b", Game: "valheim", State: model.StateStopped},
		}},
	)
}

func selectServer(app *tui.App, name string) {
	app.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	typeLine(app, name)
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})
}

// The third of DESIGN's three friction points, and the one where the friction
// is the whole feature.
func TestDeleteNeedsTheNameTyped(t *testing.T) {
	store := newStub(configuredFleet())
	app := newApp(t, store, &stubView{})
	selectServer(app, "a")

	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	if got := app.View(); !strings.Contains(got, "DELETE SERVER") {
		t.Fatalf("X did not open a confirmation:\n%s", got)
	}

	// Enter with nothing typed, and with the wrong name, both do nothing.
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	typeLine(app, "b")
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(store.deleted) != 0 {
		t.Fatalf("deleted %v before the name was right", store.deleted)
	}

	// The prompt is still open with the near miss cleared, so the right name
	// goes straight in — pressing X again would type an X into the field,
	// which is correct and not what this is testing.
	typeLine(app, "a")
	if _, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		app.Update(cmd())
	}
	if len(store.deleted) != 1 || store.deleted[0] != "a" {
		t.Errorf("deleted %v, want [a]", store.deleted)
	}
}

// The sentence that makes the key safe to bind at all.
func TestDeleteSaysTheWorldIsKept(t *testing.T) {
	app := newApp(t, newStub(configuredFleet()), &stubView{})
	selectServer(app, "a")
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})

	got := app.View()
	if !strings.Contains(got, "left exactly where it is") {
		t.Errorf("the confirmation does not say the world survives:\n%s", got)
	}
	if !strings.Contains(got, `C:\gameservers\a`) {
		t.Errorf("the confirmation does not name the data directory:\n%s", got)
	}
}

func TestDeleteEscapeCancels(t *testing.T) {
	store := newStub(configuredFleet())
	app := newApp(t, store, &stubView{})
	selectServer(app, "a")

	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	typeLine(app, "a")
	app.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if len(store.deleted) != 0 {
		t.Errorf("esc deleted %v anyway", store.deleted)
	}
	if strings.Contains(app.View(), "DELETE SERVER") {
		t.Error("esc left the confirmation up")
	}
}

// A container Garrison has no file for has nothing of its own to delete, and
// it would be found again on the next poll.
func TestDeleteRefusesAnUnconfiguredServer(t *testing.T) {
	store := newStub(snapshot()) // labelled containers, no instances
	app := newApp(t, store, &stubView{})
	selectServer(app, "a")

	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})

	if strings.Contains(app.View(), "DELETE SERVER") {
		t.Error("an unconfigured server was offered for deletion")
	}
	if len(store.notices) == 0 {
		t.Error("the refusal said nothing")
	}
}

// Comparing two servers means the same screen for each in turn, which the
// rail's arrow keys cannot do without taking focus off the stage.
func TestBracketsStepBetweenServersKeepingTheScreen(t *testing.T) {
	v := &stubView{}
	app := newApp(t, newStub(configuredFleet()), v)
	selectServer(app, "a")

	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}}) // a screen
	app.View()
	screen := v.rendered.Server
	if screen != "a" {
		t.Fatalf("frame server = %q, want a", screen)
	}

	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	app.View()
	if v.rendered.Server != "b" {
		t.Errorf("] gave frame server %q, want b", v.rendered.Server)
	}

	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
	app.View()
	if v.rendered.Server != "a" {
		t.Errorf("[ gave frame server %q, want a", v.rendered.Server)
	}
}

// A fleet is a ring you cycle, not a list you fall off the end of.
func TestSteppingWraps(t *testing.T) {
	v := &stubView{}
	app := newApp(t, newStub(configuredFleet()), v)
	selectServer(app, "b") // the last one

	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	app.View()
	if v.rendered.Server != "a" {
		t.Errorf("stepping past the end gave %q, want a wrap to a", v.rendered.Server)
	}
}
