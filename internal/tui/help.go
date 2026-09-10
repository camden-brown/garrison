package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"

	"github.com/camden-brown/garrison/internal/tui/comp"
)

// The help overlay, generated rather than written.
//
// The View contract asks every screen for its Keys, and this is the reason it
// does: a help page maintained by hand is a help page that describes the keys
// a screen used to have. The shell's own bindings are listed here because the
// shell is the one place that knows them, and the view's come from the view.

// shellKeys are the bindings that work on every screen.
//
// They are declared as key.Binding rather than as strings so they read the
// same way a view's do, and so the help and the switch that handles them can
// be compared side by side when one of them is wrong.
var shellKeys = []key.Binding{
	key.NewBinding(key.WithKeys("1", "7"), key.WithHelp("1–7", "switch screen")),
	key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "fleet")),
	key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "cycle focus")),
	key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("ctrl+P", "palette")),
	key.NewBinding(key.WithKeys(":"), key.WithHelp(":", "command line")),
	key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter (where a screen has one)")),
	key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "copy the server's join details")),
	key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new server")),
	key.NewBinding(key.WithKeys("X"), key.WithHelp("X", "delete server — keeps the world")),
	key.NewBinding(key.WithKeys("[", "]"), key.WithHelp("[ ]", "previous / next server")),
	key.NewBinding(key.WithKeys("F"), key.WithHelp("F", "ambient mode")),
	key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "this help")),
	key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
}

// helpRows is how much of the stage the overlay takes.
const helpRows = 16

// helpView renders the overlay: the shell's keys, then this screen's.
func (a *App) helpView(width int) string {
	t := a.theme

	var b strings.Builder
	b.WriteString(t.Header.Render("EVERYWHERE"))
	b.WriteString("\n")
	b.WriteString(helpColumns(t, shellKeys, comp.Inner(width)))

	if len(a.views) > 0 {
		if keys := a.views[a.active].Keys(); len(keys) > 0 {
			b.WriteString("\n\n")
			b.WriteString(t.Header.Render(strings.ToUpper(a.views[a.active].Title())))
			b.WriteString("\n")
			b.WriteString(helpColumns(t, keys, comp.Inner(width)))
		}
	}

	b.WriteString("\n\n")
	b.WriteString(t.Dim.Render("Lowercase is safe. Uppercase is destructive and always confirms."))

	return comp.Panel{
		Theme: t, Title: "HELP", Right: "any key closes",
		Width: width, Focused: true,
	}.Render(b.String())
}

// helpColumns lays bindings out in as many columns as fit.
//
// Two columns at eighty, three at a hundred and twenty. A help page that
// scrolls is a help page nobody reads to the end of.
func helpColumns(t *comp.Theme, keys []key.Binding, width int) string {
	const cell = 38

	columns := width / cell
	if columns < 1 {
		columns = 1
	}

	var b strings.Builder
	for i, k := range keys {
		h := k.Help()
		entry := comp.Pad(t.Accent.Render(comp.Pad(h.Key, 8))+t.Dim.Render(h.Desc), cell)
		b.WriteString(entry)
		if (i+1)%columns == 0 && i != len(keys)-1 {
			b.WriteString("\n")
		}
	}
	// Trailing padding on the last row would be invisible until somebody
	// copied a block of whitespace out of it.
	out := b.String()
	lines := strings.Split(out, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.Join(lines, "\n")
}
