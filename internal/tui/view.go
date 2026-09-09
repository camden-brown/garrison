package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/model"
)

// ViewID identifies a screen. It is the value the rail, the number keys and
// the help overlay all key off, so it is declared once per view and nowhere
// else.
type ViewID string

const ViewFleet ViewID = "fleet"

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

	Update(msg tea.Msg, snap core.Snapshot) (View, tea.Cmd)
	Render(f Frame, snap core.Snapshot) string
}

// Frame is the space a view has been given, plus the theme to draw it with.
// A view is told its size rather than asking, so a golden test renders at
// 120x34 and 80x24 by passing two different frames.
type Frame struct {
	Width  int
	Height int
	Theme  *Theme
	Now    time.Time
}

// ActionMsg is a view asking for something to happen. Views never hold the
// store: they emit one of these and the shell dispatches it, which is what
// keeps Render and Update pure functions of a snapshot.
type ActionMsg struct {
	Op     core.Op
	Server string
}

// Action returns a tea.Cmd that emits an ActionMsg.
func Action(op core.Op, server string) tea.Cmd {
	return func() tea.Msg { return ActionMsg{Op: op, Server: server} }
}
