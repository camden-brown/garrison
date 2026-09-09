package comp

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/camden-brown/garrison/internal/core"
)

// RailWidth is how much of the terminal the rail takes.
//
// Fixed rather than proportional: it holds server names and view names, whose
// length does not depend on how wide the window is, and a rail that grows on a
// wide monitor is just a wider column of the same short words.
const RailWidth = 26

// Focus says which part of the interface the arrow keys are driving.
type Focus uint8

const (
	FocusServers Focus = iota
	FocusViews
	FocusStage
)

// RailEntry is one line in the view list.
type RailEntry struct {
	Title string
	Key   string // the number or letter that jumps to it
	// Reason is why the entry is unavailable, empty when it is fine. The
	// view answers this itself, so no capability knowledge lives here.
	Reason string
}

// Rail is the navigation column: which server, and which view of it.
//
// It draws both lists and nothing else. Every value comes in as an argument,
// so a rail is a pure function of the snapshot plus where the cursor is —
// which is what lets the whole shell be golden-tested.
type Rail struct {
	Theme  *Theme
	Height int

	Servers  []core.Server
	Selected string // instance name, empty when the fleet entry is selected
	Entries  []RailEntry
	Active   int // index into Entries, or -1 when the fleet screen is showing
	Focus    Focus
}

// blank is a full-width empty line. Every line the rail emits is exactly
// RailWidth cells, separators included, so the stage joined beside it starts
// in the same column on every row.
func blank() string { return strings.Repeat(" ", RailWidth) }

// cells is a string's display width. Not len: the rail is full of multibyte
// runes — the state glyphs, the cursor, the em dash standing in for "no
// players" — and byte arithmetic makes the stage beside it start in a
// different column on every row.
func cells(s string) int { return lipgloss.Width(s) }

// Render draws the rail as one string of Height lines, each exactly RailWidth
// cells wide so the stage beside it starts in the same column on every row.
func (r Rail) Render() string {
	lines := make([]string, 0, r.Height)

	lines = append(lines, r.section("FLEET", itoa(len(r.Servers)), r.Focus == FocusServers))
	lines = append(lines, r.entry("All servers", "f", r.Selected == "", r.Focus == FocusServers && r.Selected == "", false))
	for _, srv := range r.Servers {
		lines = append(lines, r.serverLine(srv))
	}

	lines = append(lines, blank())
	lines = append(lines, r.section("VIEW", r.viewSubtitle(), r.Focus == FocusViews))
	for i, e := range r.Entries {
		lines = append(lines, r.entry(e.Title, e.Key, i == r.Active, r.Focus == FocusViews && i == r.Active, e.Reason != ""))
	}

	lines = append(lines, blank())
	lines = append(lines, r.section("SCHEDULE", "", false))
	// Nothing schedules anything until the task engine at M2. Saying so beats
	// an empty heading that looks like a rendering fault.
	lines = append(lines, r.Theme.Dim.Render(Pad("  none until M2", RailWidth)))

	for len(lines) < r.Height {
		lines = append(lines, blank())
	}
	return strings.Join(lines[:r.Height], "\n")
}

// viewSubtitle names the server the view list applies to, because "Dashboard"
// on its own does not say whose.
func (r Rail) viewSubtitle() string {
	if r.Selected == "" {
		return "no server"
	}
	return r.Selected
}

func (r Rail) section(label, right string, focused bool) string {
	style := r.Theme.Header
	if focused {
		style = r.Theme.Accent
	}

	gap := RailWidth - cells(label) - cells(right)
	if gap < 1 {
		gap = 1
	}
	return style.Render(Truncate(label, RailWidth)) +
		strings.Repeat(" ", gap) +
		r.Theme.Dim.Render(right)
}

// entry is one selectable line: a cursor, a name, and the key that jumps to it.
func (r Rail) entry(title, key string, selected, focused, unavailable bool) string {
	cursor := "  "
	if selected {
		marker := "▌"
		if r.Theme.ASCII {
			marker = ">"
		}
		cursor = r.Theme.Accent.Render(marker) + " "
	}

	name := title
	style := r.Theme.Dim
	switch {
	case unavailable:
		// Dimmer than the rest: still reachable, and honest that there is
		// nothing behind it yet.
		style = r.Theme.Dim
		name += " ·"
	case focused:
		style = r.Theme.Selected
	case selected:
		style = r.Theme.Title
	}

	width := RailWidth - 2 - cells(key) - 1
	return cursor + style.Render(Pad(name, width)) + " " + r.Theme.Dim.Render(key)
}

func (r Rail) serverLine(srv core.Server) string {
	glyph := r.Theme.StateStyle(srv.State).Render(r.Theme.StateGlyph(srv.State))

	right := "—"
	if srv.State.Live() && len(srv.Players) > 0 {
		right = itoa(len(srv.Players))
	}

	name := srv.Name
	style := r.Theme.Dim
	if srv.Name == r.Selected {
		style = r.Theme.Title
		if r.Focus == FocusServers {
			style = r.Theme.Selected
		}
	}

	width := RailWidth - 2 - cells(right) - 1
	return glyph + " " + style.Render(Pad(name, width)) + " " + r.Theme.Dim.Render(right)
}
