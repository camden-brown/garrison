// Package fleet is the Fleet view: every server on one line.
//
// It is the default screen and the one left on the monitor, so it answers one
// question without scrolling — is anything wrong — and offers the two verbs
// that fix the usual answer.
package fleet

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tui"
)

// View lists the fleet.
//
// Cursor is the only state it owns. Everything drawn comes from the snapshot,
// which is what lets the shell rebuild this on a resize without losing the
// operator's place — and what makes a golden render a fixed snapshot in, a
// fixed string out.
type View struct {
	cursor  int
	confirm string // instance awaiting a stop confirmation, empty when none
}

// New returns the Fleet view.
func New() *View { return &View{} }

func (v *View) ID() tui.ViewID { return tui.ViewFleet }
func (v *View) Title() string  { return "Fleet" }

// Available: the fleet is never unavailable — it is the screen that explains
// why everything else is.
func (v *View) Available(model.Instance) (bool, string) { return true, "" }

var (
	keyUp      = key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up"))
	keyDown    = key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down"))
	keyStart   = key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "start"))
	keyStop    = key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "stop"))
	keyConfirm = key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "confirm"))
	keyCancel  = key.NewBinding(key.WithKeys("n", "esc"), key.WithHelp("n/esc", "cancel"))
)

func (v *View) Keys() []key.Binding {
	return []key.Binding{keyUp, keyDown, keyStart, keyStop}
}

func (v *View) Update(msg tea.Msg, snap core.Snapshot) (tui.View, tea.Cmd) {
	msgKey, ok := msg.(tea.KeyMsg)
	if !ok {
		return v, nil
	}

	next := *v
	next.clampCursor(snap)

	// A pending confirmation swallows every other key. Answering it is the
	// only thing the view will do until it is answered.
	if next.confirm != "" {
		switch {
		case key.Matches(msgKey, keyConfirm):
			server := next.confirm
			next.confirm = ""
			return &next, tui.Action(core.OpStop, server)
		case key.Matches(msgKey, keyCancel):
			next.confirm = ""
		}
		return &next, nil
	}

	switch {
	case key.Matches(msgKey, keyUp):
		next.cursor--
	case key.Matches(msgKey, keyDown):
		next.cursor++
	case key.Matches(msgKey, keyStart):
		if srv, ok := next.selected(snap); ok {
			return &next, tui.Action(core.OpStart, srv.Name)
		}
	case key.Matches(msgKey, keyStop):
		// Uppercase, so it confirms. Stopping is not world-losing and does
		// not ask for the server's name typed out — that friction is
		// reserved for the three actions that can destroy a save.
		if srv, ok := next.selected(snap); ok {
			next.confirm = srv.Name
		}
	}
	next.clampCursor(snap)
	return &next, nil
}

func (v *View) clampCursor(snap core.Snapshot) {
	if v.cursor >= len(snap.Servers) {
		v.cursor = len(snap.Servers) - 1
	}
	if v.cursor < 0 {
		v.cursor = 0
	}
}

func (v *View) selected(snap core.Snapshot) (core.Server, bool) {
	if v.cursor < 0 || v.cursor >= len(snap.Servers) {
		return core.Server{}, false
	}
	return snap.Servers[v.cursor], true
}

// columns is the width budget for one row, recomputed per frame.
//
// Every view has a real narrow layout rather than a clipped wide one, so the
// columns that stop earning their space are dropped rather than squeezed:
// ports go first, then the note.
type columns struct {
	name, game, state, uptime, ports, note int
}

func layout(width int) columns {
	c := columns{name: 20, game: 12, state: 12, uptime: 10, ports: 20}

	switch {
	case width >= 110:
	case width >= 84:
		c.ports = 14
	default:
		c.ports = 0
		c.name = 16
		c.game = 10
	}

	// 2 cells of cursor gutter, then one space after every column that
	// precedes the note. The glyph lives inside the state column so the
	// header lines up with the rows without a special case.
	seps := 4
	if c.ports > 0 {
		seps = 5
	}
	c.note = width - (2 + c.name + c.game + c.state + c.uptime + c.ports + seps)
	if c.note < 0 {
		c.note = 0
	}
	return c
}

// stateCell is the glyph and the word in one column, so the two can never
// drift apart and the header only has to describe one field.
func stateCell(t *tui.Theme, state model.State, width int) string {
	glyph := t.StateGlyph(state)
	word := tui.Pad(t.StateWord(state), width-2)
	return t.StateStyle(state).Render(glyph + " " + word)
}

func (v *View) Render(f tui.Frame, snap core.Snapshot) string {
	t := f.Theme
	cols := layout(f.Width)

	var b strings.Builder
	b.WriteString(t.Title.Render("SERVERS"))
	b.WriteString("  ")
	b.WriteString(t.Dim.Render(hints(v.confirm != "")))
	b.WriteString("\n\n")

	if !snap.Engine.OK {
		b.WriteString(t.Err.Render(engineBanner(snap.Engine)))
		b.WriteString("\n\n")
	}

	if len(snap.Servers) == 0 {
		b.WriteString(emptyExplanation(t, snap))
		b.WriteString("\n")
		return b.String()
	}

	b.WriteString(t.Header.Render(header(cols)))
	b.WriteString("\n")

	for i, srv := range snap.Servers {
		b.WriteString(v.row(f, cols, srv, i == v.cursor))
		b.WriteString("\n")
	}

	if v.confirm != "" {
		b.WriteString("\n")
		b.WriteString(t.Accent.Render("stop " + v.confirm + "?  y / n"))
		b.WriteString("\n")
	}

	if n := latestError(snap); n != "" {
		b.WriteString("\n")
		b.WriteString(t.Err.Render(tui.Truncate(n, f.Width)))
		b.WriteString("\n")
	}

	return b.String()
}

func hints(confirming bool) string {
	if confirming {
		return "y confirm · n cancel"
	}
	return "u start · S stop · ↑↓ move · q quit"
}

func header(c columns) string {
	var b strings.Builder
	b.WriteString("  ")
	b.WriteString(tui.Pad("NAME", c.name))
	b.WriteString(" ")
	b.WriteString(tui.Pad("GAME", c.game))
	b.WriteString(" ")
	b.WriteString(tui.Pad("STATE", c.state))
	b.WriteString(" ")
	b.WriteString(tui.Pad("UPTIME", c.uptime))
	if c.ports > 0 {
		b.WriteString(" ")
		b.WriteString(tui.Pad("PORTS", c.ports))
	}
	b.WriteString(" ")
	b.WriteString(tui.Pad("NOTE", c.note))
	return strings.TrimRight(b.String(), " ")
}

func (v *View) row(f tui.Frame, c columns, srv core.Server, selected bool) string {
	t := f.Theme

	cursor := "  "
	if selected {
		cursor = t.Accent.Render("▌") + " "
		if t.ASCII {
			cursor = t.Accent.Render(">") + " "
		}
	}

	name := tui.Pad(srv.Name, c.name)
	if selected {
		name = t.Selected.Render(name)
	}

	var b strings.Builder
	b.WriteString(cursor)
	b.WriteString(name)
	b.WriteString(" ")
	b.WriteString(tui.Pad(srv.Game, c.game))
	b.WriteString(" ")
	// State is glyph plus word, both of them, always. Colour is the third
	// carrier and never the only one.
	b.WriteString(stateCell(t, srv.State, c.state))
	b.WriteString(" ")
	b.WriteString(t.Dim.Render(tui.Pad(tui.Duration(srv.Uptime(f.Now)), c.uptime)))
	if c.ports > 0 {
		b.WriteString(" ")
		b.WriteString(t.Dim.Render(tui.Pad(portList(srv.Ports), c.ports)))
	}
	b.WriteString(" ")
	b.WriteString(noteCell(t, srv, c.note))
	return strings.TrimRight(b.String(), " ")
}

// noteCell is what the row has to say for itself: an operation in flight beats
// a stale reason, because the operator just pressed the key that caused it.
func noteCell(t *tui.Theme, srv core.Server, width int) string {
	if srv.Busy != core.OpNone {
		return t.Accent.Render(tui.Pad(busyText(srv), width))
	}
	if srv.Detail != "" {
		style := t.Dim
		switch {
		case srv.State == model.StateCrashed, srv.State == model.StateUnknown:
			style = t.Err
		case srv.State == model.StateStopped && srv.ExitCode != 0:
			// Deliberately down, but it did not go quietly. The glyph stays
			// grey because the state is honest — Garrison asked for this —
			// while the reason stays loud, because a server killed before it
			// finished writing is a save you may not have.
			style = t.Err
		}
		return style.Render(tui.Pad(srv.Detail, width))
	}
	if !srv.Health.OK && srv.Health.Detail != "" {
		return t.Err.Render(tui.Pad("unhealthy: "+srv.Health.Detail, width))
	}
	return tui.Pad("", width)
}

// busyText says what is happening and, for a stop, how long it may take.
//
// A game server is asked to leave and then given time to finish writing, so a
// stop is a wait rather than an instant — sixty seconds of an unqualified
// "stopping…" is how an operator concludes it has hung and reaches for
// something less patient than the grace period.
func busyText(srv core.Server) string {
	if srv.Busy == core.OpStop && srv.StopGrace > 0 {
		return "stopping… up to " + core.Budget(srv.StopGrace)
	}
	return srv.Busy.Present() + "…"
}

func portList(ports []model.PortMap) string {
	if len(ports) == 0 {
		return "—"
	}
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		out = append(out, strconv.Itoa(p.Host)+"/"+proto(p.Container))
	}
	return strings.Join(out, " ")
}

func proto(spec string) string {
	if _, p, ok := strings.Cut(spec, "/"); ok {
		return p
	}
	return "tcp"
}

func engineBanner(e core.Engine) string {
	transport := e.Transport
	if transport == "" {
		transport = "docker"
	}
	msg := "! " + transport + " unreachable"
	if e.Err != "" {
		msg += " — " + e.Err
	}
	return msg
}

// emptyExplanation is the difference between a view that looks broken and one
// that tells you what to do. An empty fleet has two causes and they need
// different answers.
func emptyExplanation(t *tui.Theme, snap core.Snapshot) string {
	if !snap.Engine.OK {
		return t.Dim.Render("Nothing to show until the engine answers.") + "\n" +
			t.Dim.Render("Garrison keeps trying; it will fill in on its own.")
	}
	return t.Dim.Render("No containers labelled garrison.managed=1.") + "\n" +
		t.Dim.Render("The fleet is found by label, so anything Garrison created will appear here.")
}

func latestError(snap core.Snapshot) string {
	for i := len(snap.Notices) - 1; i >= 0; i-- {
		if snap.Notices[i].Level == core.LevelError {
			return snap.Notices[i].Text
		}
	}
	return ""
}
