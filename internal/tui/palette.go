package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/tui/comp"
)

// paletteRows is how many candidates are shown at once. Enough to see the
// shape of what matched, few enough that the palette does not become the
// screen.
const paletteRows = 8

// entryKind says what selecting a palette row does.
type entryKind uint8

const (
	entryServer entryKind = iota
	entryView
	entryVerb
)

// entry is one palette candidate.
type entry struct {
	kind  entryKind
	label string // what is shown and matched against
	note  string // the dim half: what kind of thing this is

	server string
	view   int
	op     core.Op
}

// palette is the Ctrl+P overlay: one list over servers, views and verbs.
//
// Matching is a subsequence rather than a substring, which is the opposite of
// what the fleet's filter does, and deliberately. A filter hides rows and acts
// on whatever is left, so an invisible match rule is a hazard; a palette shows
// you the entry you are about to run and waits for enter. "vh" reaching
// "valheim-huldra" is worth having when the alternative is typing the whole
// name.
func (a *App) paletteEntries() []entry {
	var out []entry

	for _, srv := range a.snap.Servers {
		out = append(out, entry{kind: entryServer, label: srv.Name, note: "server", server: srv.Name})
	}
	for i, v := range a.views {
		out = append(out, entry{kind: entryView, label: v.Title(), note: "view", view: i})
	}
	for _, v := range verbs {
		label := v.name
		if a.selected != "" {
			label = v.name + " " + a.selected
		}
		out = append(out, entry{kind: entryVerb, label: label, note: v.help, op: v.op, server: a.selected})
	}
	return out
}

// paletteMatches is the entries the query selects, best first.
func (a *App) paletteMatches() []entry {
	all := a.paletteEntries()
	if a.paletteQuery == "" {
		return all
	}
	out := make([]entry, 0, len(all))
	for _, e := range all {
		if subsequence(strings.ToLower(e.label), strings.ToLower(a.paletteQuery)) {
			out = append(out, e)
		}
	}
	return out
}

// subsequence reports whether every rune of needle appears in haystack in
// order, which is what makes "vh" find "valheim-huldra".
func subsequence(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	want := []rune(needle)
	at := 0
	for _, r := range haystack {
		if r == want[at] {
			at++
			if at == len(want) {
				return true
			}
		}
	}
	return false
}

// paletteKey handles a keystroke while the palette is open.
func (a *App) paletteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	matches := a.paletteMatches()

	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlP:
		a.closePalette()
		return a, nil

	case tea.KeyUp:
		a.paletteCursor--
		a.clampPalette(len(matches))
		return a, nil

	case tea.KeyDown:
		a.paletteCursor++
		a.clampPalette(len(matches))
		return a, nil

	case tea.KeyEnter:
		if len(matches) == 0 {
			a.closePalette()
			return a, nil
		}
		chosen := matches[min(a.paletteCursor, len(matches)-1)]
		a.closePalette()
		return a, a.runEntry(chosen)
	}

	if query, caret, handled := comp.EditKey(a.paletteQuery, a.paletteCaret, msg); handled {
		a.paletteQuery, a.paletteCaret = query, caret
		// A narrowed list with the cursor left near the bottom would point
		// at nothing, so every edit returns to the top match.
		a.paletteCursor = 0
	}
	return a, nil
}

func (a *App) closePalette() {
	a.palette, a.paletteQuery, a.paletteCaret, a.paletteCursor = false, "", 0, 0
}

func (a *App) clampPalette(n int) {
	if a.paletteCursor >= n {
		a.paletteCursor = n - 1
	}
	if a.paletteCursor < 0 {
		a.paletteCursor = 0
	}
}

// runEntry does what a chosen row says.
func (a *App) runEntry(e entry) tea.Cmd {
	switch e.kind {
	case entryServer:
		a.selected = e.server
		a.ensureServerSelected()
		return nil

	case entryView:
		if e.view < len(a.views) {
			a.active = e.view
			a.ensureServerSelected()
		}
		return nil

	case entryVerb:
		if e.server == "" {
			a.store.Notify(a.ctx, "", "\""+e.label+"\" needs a server: pick one first.")
			return nil
		}
		return Action(e.op, e.server)
	}
	return nil
}

// paletteView renders the overlay.
func (a *App) paletteView(width int) string {
	t := a.theme
	matches := a.paletteMatches()
	a.clampPalette(len(matches))

	var b strings.Builder
	b.WriteString(t.Accent.Render("> ") + comp.Input{
		Value: a.paletteQuery, Cursor: a.paletteCaret, Width: comp.Inner(width) - 2, Theme: t,
	}.Render())
	b.WriteString("\n")

	if len(matches) == 0 {
		b.WriteString(t.Dim.Render("Nothing matching."))
		return comp.Panel{Theme: t, Title: "PALETTE", Width: width, Focused: true}.
			Render(strings.TrimRight(b.String(), "\n"))
	}

	shown := matches
	if len(shown) > paletteRows {
		shown = shown[:paletteRows]
	}
	for i, e := range shown {
		selected := i == a.paletteCursor
		marker := "  "
		if selected {
			marker = "▌ "
			if t.ASCII {
				marker = "> "
			}
		}
		b.WriteString(t.On(t.Accent, selected).Render(marker))
		b.WriteString(t.On(t.Title, selected).Render(comp.Pad(e.label, 28)))
		b.WriteString(t.On(t.Dim, selected).Render(comp.Truncate(e.note, comp.Inner(width)-30)))
		b.WriteString("\n")
	}

	if len(matches) > len(shown) {
		b.WriteString(t.Dim.Render(itoa(len(matches)-len(shown)) + " more"))
	}

	return comp.Panel{Theme: t, Title: "PALETTE", Right: "enter runs · esc closes", Width: width, Focused: true}.
		Render(strings.TrimRight(b.String(), "\n"))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
