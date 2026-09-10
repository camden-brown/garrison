package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tui/comp"
)

// ViewID identifies a screen. It is the value the rail, the number keys and
// the help overlay all key off, so it is declared once per view and nowhere
// else.
type ViewID string

// The nine screens from DESIGN §3. The order here is the rail order and the
// number keys, so it is declared once and everything derives from it.
const (
	ViewFleet     ViewID = "fleet"
	ViewDashboard ViewID = "dashboard"
	ViewConsole   ViewID = "console"
	ViewPlayers   ViewID = "players"
	ViewMods      ViewID = "mods"
	ViewSettings  ViewID = "settings"
	ViewTasks     ViewID = "tasks"
	ViewBackups   ViewID = "backups"
)

// View is the contract every screen implements.
//
// Left alone, an app shell grows a switch per feature — one for rendering, one
// for live keys, one for titles — and every new screen means editing all of
// them and forgetting one. So views register in a slice and the shell knows
// only this interface. Adding a screen is a package and a line.
//
// Update returns a View rather than mutating, so a view is a value: the shell
// can rebuild one on resize without it losing anything, and a test can render
// one without constructing a program.
type View interface {
	ID() ViewID
	Title() string

	// Keys are the bindings this view handles, for the hint line and the
	// help overlay. Declaring them is what keeps the two in step with what
	// the view actually does.
	Keys() []key.Binding

	// Available gates the view on what the game can do, returning the
	// sentence to show when it cannot: the Mods view itself answers
	// "Palworld has no mod system", so no capability knowledge leaks into
	// the shell.
	//
	// Fleet-wide screens ignore the instance. From M1 the shell passes the
	// selected server's.
	Available(inst model.Instance) (bool, string)

	// Update takes the Frame as well as the message, so a view never has to
	// remember what it was last told. DESIGN §7 writes this without the
	// Frame; that version only works because Bubble Tea happens to render
	// before every Update, and a view that stashes the selection during
	// Render to use during Update is one refactor away from acting on a
	// stale one.
	Update(msg tea.Msg, f Frame, snap core.Snapshot) (View, tea.Cmd)
	Render(f Frame, snap core.Snapshot) string
}

// Frame is the space a view has been given, plus the theme to draw it with.
// A view is told its size rather than asking, so a golden test renders at
// 120x34 and 80x24 by passing two different frames.
type Frame struct {
	Width  int
	Height int
	Theme  *comp.Theme
	Now    time.Time

	// Server is the instance the rail has selected, empty when the fleet is.
	// Selection belongs to the shell rather than to each view: the rail and
	// the stage both show it, and two cursors that can disagree about which
	// server you are looking at is a bug waiting for a busy evening.
	Server string

	// Focused says whether the stage has keyboard focus. A view draws its
	// own cursor differently when the rail has it, so it is always clear
	// which set of arrow keys you are pressing.
	Focused bool
}

// ActionMsg is a view asking for something to happen. Views never hold the
// store: they emit one of these and the shell dispatches it, which is what
// keeps Render and Update pure functions of a snapshot.
type ActionMsg struct {
	Op     core.Op
	Server string

	// Recreate is set on an apply that needs a new container. The settings
	// view works it out from the game's Schema, because it is the only
	// thing that knows what a setting costs.
	Recreate bool

	// Archive is the backup a restore reads from, empty for every other op.
	Archive string
}

// Action returns a tea.Cmd that emits an ActionMsg.
func Action(op core.Op, server string) tea.Cmd {
	return func() tea.Msg { return ActionMsg{Op: op, Server: server} }
}

// CommandMsg is the console sending a command to a server.
//
// A message rather than a call for the same reason every other action is one:
// the view stays a pure function of a snapshot, and what comes back arrives as
// console output rather than as a return value the view would have to hold.
type CommandMsg struct {
	Server string
	Text   string
}

// Command returns a tea.Cmd that sends one console command.
func Command(server, text string) tea.Cmd {
	return func() tea.Msg { return CommandMsg{Server: server, Text: text} }
}

// Restore returns a tea.Cmd that replaces a server's world with an archive.
//
// The archive travels with the message because the store has no notion of a
// selected row: the view knows which backup the cursor was on, and naming it
// here is what keeps the store from having to hold a cursor on the view's
// behalf.
func Restore(server, archive string) tea.Cmd {
	return func() tea.Msg {
		return ActionMsg{Op: core.OpRestore, Server: server, Archive: archive}
	}
}

// Apply returns a tea.Cmd that applies a server's pending settings.
func Apply(server string, recreate bool) tea.Cmd {
	return func() tea.Msg {
		return ActionMsg{Op: core.OpApply, Server: server, Recreate: recreate}
	}
}

// EditMsg is a view changing a setting. The shell records it in the store,
// because the draft outlives the screen: applying it is a task, and a task
// cannot reach into a view.
type EditMsg struct {
	Server string
	Key    string
	Value  any
}

// Edit returns a tea.Cmd that emits an EditMsg.
func Edit(server, key string, value any) tea.Cmd {
	return func() tea.Msg { return EditMsg{Server: server, Key: key, Value: value} }
}

// DiscardMsg throws away a server's unapplied changes.
type DiscardMsg struct{ Server string }

// Discard returns a tea.Cmd that emits a DiscardMsg.
func Discard(server string) tea.Cmd {
	return func() tea.Msg { return DiscardMsg{Server: server} }
}

// CancelTaskMsg asks the engine to stop a task. It compensates rather than
// simply stopping, so the world is left where it started.
type CancelTaskMsg struct{ ID string }

// CancelTask returns a tea.Cmd that emits a CancelTaskMsg.
func CancelTask(id string) tea.Cmd {
	return func() tea.Msg { return CancelTaskMsg{ID: id} }
}

// SelectMsg is a view asking the shell to select a different server. The
// Fleet table and the rail's server list are the same selection seen twice, so
// moving in either has to move both.
type SelectMsg struct{ Server string }

// Select returns a tea.Cmd that emits a SelectMsg.
func Select(server string) tea.Cmd {
	return func() tea.Msg { return SelectMsg{Server: server} }
}
