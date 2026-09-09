package tui

import (
	"context"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/model"
)

// Store is what the shell needs from internal/core. It is an interface so the
// app can be driven by a stub in a test without standing up a writer
// goroutine.
type Store interface {
	Subscribe() <-chan core.Snapshot
	Snapshot() core.Snapshot
	Start(ctx context.Context, instance string)
	Stop(ctx context.Context, instance string)
}

// App is the shell: it owns the terminal, routes keys to the current view, and
// turns a view's ActionMsg into a call on the store.
//
// It contains no per-screen knowledge. Everything a screen does is behind the
// View interface, so adding one does not touch this file.
type App struct {
	store  Store
	ctx    context.Context
	views  []View
	active int

	snap   core.Snapshot
	sub    <-chan core.Snapshot
	theme  *Theme
	width  int
	height int
	now    func() time.Time
}

// NewApp builds the shell over a store and a set of views.
func NewApp(ctx context.Context, store Store, theme *Theme, views ...View) *App {
	return &App{
		store: store,
		ctx:   ctx,
		views: views,
		theme: theme,
		snap:  store.Snapshot(),
		// A sane size before the first WindowSizeMsg arrives, so the very
		// first frame is not rendered into a zero-width terminal.
		width:  120,
		height: 34,
		now:    time.Now,
	}
}

func (a *App) Init() tea.Cmd {
	a.sub = a.store.Subscribe()
	return waitForSnapshot(a.sub)
}

// snapshotMsg carries a published snapshot into the Bubble Tea loop. The store
// publishes on its own goroutine and the UI receives here, which is the only
// place the two meet.
type snapshotMsg struct {
	snap core.Snapshot
	ok   bool
}

func waitForSnapshot(sub <-chan core.Snapshot) tea.Cmd {
	return func() tea.Msg {
		snap, ok := <-sub
		return snapshotMsg{snap: snap, ok: ok}
	}
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		return a, nil

	case snapshotMsg:
		if !msg.ok {
			// The store shut down. Nothing will publish again, so stop
			// rather than spin on a closed channel.
			return a, tea.Quit
		}
		a.snap = msg.snap
		return a, waitForSnapshot(a.sub)

	case ActionMsg:
		return a, a.dispatch(msg)

	case tea.KeyMsg:
		switch k := msg.String(); k {
		case "ctrl+c", "q":
			return a, tea.Quit
		case "f":
			a.show(ViewFleet)
			return a, nil
		case "1":
			a.show(ViewDashboard)
			return a, nil
		case "tab":
			a.active = (a.active + 1) % len(a.views)
			return a, nil
		}
		return a.routeToView(msg)
	}

	return a.routeToView(msg)
}

// show switches to a view by id. Unknown ids are ignored rather than
// panicking: the keymap and the registry are edited separately and drifting
// apart should not take the program down.
func (a *App) show(id ViewID) {
	for i, v := range a.views {
		if v.ID() == id {
			a.active = i
			return
		}
	}
}

func (a *App) routeToView(msg tea.Msg) (tea.Model, tea.Cmd) {
	if len(a.views) == 0 {
		return a, nil
	}
	next, cmd := a.views[a.active].Update(msg, a.snap)
	a.views[a.active] = next
	return a, cmd
}

// dispatch turns a view's request into work. This is the only place the shell
// touches the store's write side, and it never blocks: the store records the
// operation and reports back as a snapshot.
func (a *App) dispatch(msg ActionMsg) tea.Cmd {
	switch msg.Op {
	case core.OpStart:
		a.store.Start(a.ctx, msg.Server)
	case core.OpStop:
		a.store.Stop(a.ctx, msg.Server)
	}
	return nil
}

func (a *App) View() string {
	if len(a.views) == 0 {
		return "no views registered\n"
	}

	view := a.views[a.active]
	// One line of status bar, one blank line above it.
	stage := Frame{
		Width:  a.width,
		Height: a.height - 2,
		Theme:  a.theme,
		Now:    a.now(),
	}

	body := view.Render(stage, a.snap)
	return body + "\n" + a.statusBar()
}

// statusBar never changes position. It is the one thing on screen whose
// location you can rely on, so it carries the facts you look for without
// reading: fleet health, engine state, the clock.
func (a *App) statusBar() string {
	t := a.theme
	up, down, unknown := a.snap.Counts()

	label := " FLEET "
	if len(a.views) > 0 {
		label = " " + strings.ToUpper(a.views[a.active].Title()) + " "
	}

	left := t.Bar.Render(label) + "  " +
		strconv.Itoa(len(a.snap.Servers)) + " servers · " +
		t.StateStyle(model.StateRunning).Render(strconv.Itoa(up)+" up")

	if down > 0 {
		left += " · " + t.Dim.Render(strconv.Itoa(down)+" down")
	}
	if unknown > 0 {
		left += " · " + t.StateStyle(model.StateUnknown).Render(strconv.Itoa(unknown)+" unknown")
	}

	right := a.engineWord() + "  " + a.now().Format("15:04")

	// Width, not len: both halves already carry escape sequences.
	gap := a.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func (a *App) engineWord() string {
	e := a.snap.Engine
	transport := e.Transport
	if transport == "" {
		transport = "docker"
	}
	if e.OK {
		return a.theme.Dim.Render(transport + " · healthy")
	}
	return a.theme.Err.Render(transport + " · unreachable")
}
