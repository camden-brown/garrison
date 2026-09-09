package tui_test

import (
	"context"
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
)

func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii)
	m.Run()
}

var now = time.Date(2026, 9, 9, 21, 7, 0, 0, time.UTC)

// stubStore stands in for internal/core so the shell can be driven without a
// writer goroutine.
type stubStore struct {
	snap    core.Snapshot
	ch      chan core.Snapshot
	started []string
	stopped []string
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

// stubView records what it was handed and emits whatever it is told to.
type stubView struct {
	rendered tui.Frame
	emit     tea.Cmd
}

func (v *stubView) ID() tui.ViewID                          { return tui.ViewFleet }
func (v *stubView) Title() string                           { return "Stub" }
func (v *stubView) Keys() []key.Binding                     { return nil }
func (v *stubView) Available(model.Instance) (bool, string) { return true, "" }

func (v *stubView) Update(msg tea.Msg, _ core.Snapshot) (tui.View, tea.Cmd) {
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
	app := tui.NewApp(context.Background(), store, tui.NewTheme(false), v)
	app.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return app
}

// The status bar never changes position, so what it says has to be right
// without being read closely. It names the screen you are on, so the one
// fixed thing on the display also answers "where am I".
func TestStatusBarSummarisesTheFleet(t *testing.T) {
	app := newApp(t, newStub(snapshot()), &stubView{})

	out := app.View()
	for _, want := range []string{"STUB", "3 servers", "1 up", "2 down", "npipe", "healthy"} {
		if !strings.Contains(out, want) {
			t.Errorf("status bar is missing %q:\n%s", want, out)
		}
	}
}

func TestStatusBarSaysUnreachableRatherThanNothing(t *testing.T) {
	snap := core.Reduce(snapshot(), core.FleetUnobservable{At: now})
	app := newApp(t, newStub(snap), &stubView{})

	out := app.View()
	if !strings.Contains(out, "unreachable") {
		t.Errorf("status bar does not report the engine:\n%s", out)
	}
	if !strings.Contains(out, "3 unknown") {
		t.Errorf("status bar does not report the fleet as unknown:\n%s", out)
	}
}

// The stage gets the terminal minus the status bar. A view told it has the
// whole height draws a row underneath the bar and the bar scrolls away.
func TestTheViewIsGivenRoomForTheStatusBar(t *testing.T) {
	v := &stubView{}
	newApp(t, newStub(snapshot()), v).View()

	if v.rendered.Width != 120 {
		t.Errorf("frame width = %d, want 120", v.rendered.Width)
	}
	if v.rendered.Height >= 34 {
		t.Errorf("frame height = %d, want less than the terminal's 34", v.rendered.Height)
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
	app := tui.NewApp(context.Background(), newStub(snapshot()), tui.NewTheme(false), first, second)
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
	app := tui.NewApp(context.Background(), newStub(snapshot()), tui.NewTheme(false), only)
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
func (v *namedView) Update(msg tea.Msg, s core.Snapshot) (tui.View, tea.Cmd) {
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

	app2 := tui.NewApp(context.Background(), store, tui.NewTheme(false), &stubView{})
	cmd := app2.Init()
	_, quit := app2.Update(cmd())
	if quit == nil {
		t.Fatal("no command after the subscription closed")
	}
	if _, ok := quit().(tea.QuitMsg); !ok {
		t.Error("the shell did not stop when its store went away")
	}
}
