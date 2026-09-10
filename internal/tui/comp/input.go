package comp

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

// Input is a single-line text editor.
//
// It holds nothing, which is the whole design. The text being edited lives
// where the rest of the state lives — for the settings form that is the
// store's draft — and only the cursor position is passed in by the view.
// That is the split DESIGN asks for: views own cursor, scroll and filter and
// nothing else, so rebuilding a view on resize costs you a cursor position
// nobody notices and never a half-typed value.
//
// The consequence is that this type is a renderer and EditKey is a pure
// function, both of which can be tested without a program, a terminal or a
// store.
type Input struct {
	Value  string
	Cursor int // rune index, 0..len([]rune(Value))
	Width  int
	Theme  *Theme
}

// Render draws the value with a block cursor, scrolled so the cursor is
// always on screen.
//
// The scroll is by display width rather than rune count, because a value with
// CJK or emoji in it would otherwise walk out of the field one wide rune at a
// time — the same reason Truncate exists.
func (in Input) Render() string {
	if in.Width <= 0 {
		return ""
	}

	// One extra cell so the cursor has somewhere to sit past the last rune.
	cells := append([]rune(in.Value), ' ')
	cursor := clampInt(in.Cursor, 0, len(cells)-1)

	// Scroll right until the cursor's cell fits, then take as much as the
	// field can show from there.
	start := 0
	for start < cursor && widthOf(cells[start:cursor+1]) > in.Width {
		start++
	}
	end, w := start, 0
	for end < len(cells) {
		rw := runewidth.RuneWidth(cells[end])
		if w+rw > in.Width {
			break
		}
		w += rw
		end++
	}
	if end <= cursor {
		end = cursor + 1
	}

	before := string(cells[start:cursor])
	at := string(cells[cursor : cursor+1])
	after := ""
	if cursor+1 < end {
		after = string(cells[cursor+1 : end])
	}

	// The cursor is the selected style rather than a real terminal cursor:
	// the shell draws several panes and only one of them is focused, so a
	// cursor that is part of the paint is the one that ends up in the right
	// place — and in a golden render.
	out := before + in.Theme.Selected.Render(at) + after
	return Pad(out, in.Width)
}

// EditKey applies one keypress to a value and cursor.
//
// It reports whether it consumed the key, so a caller can fall through to its
// own bindings for anything text editing does not claim — which is how esc and
// enter stay the caller's to interpret.
func EditKey(value string, cursor int, msg tea.KeyMsg) (string, int, bool) {
	runes := []rune(value)
	cursor = clampInt(cursor, 0, len(runes))

	insert := func(add []rune) (string, int, bool) {
		out := make([]rune, 0, len(runes)+len(add))
		out = append(out, runes[:cursor]...)
		out = append(out, add...)
		out = append(out, runes[cursor:]...)
		return string(out), cursor + len(add), true
	}

	switch msg.Type {
	case tea.KeyRunes:
		return insert(msg.Runes)

	case tea.KeySpace:
		// Space arrives as its own type rather than in Runes, and a form
		// field that cannot hold "My Server" is not a text field.
		return insert([]rune{' '})

	case tea.KeyBackspace:
		if cursor == 0 {
			return value, cursor, true
		}
		return string(append(append([]rune{}, runes[:cursor-1]...), runes[cursor:]...)), cursor - 1, true

	case tea.KeyDelete:
		if cursor >= len(runes) {
			return value, cursor, true
		}
		return string(append(append([]rune{}, runes[:cursor]...), runes[cursor+1:]...)), cursor, true

	case tea.KeyLeft:
		return value, clampInt(cursor-1, 0, len(runes)), true

	case tea.KeyRight:
		return value, clampInt(cursor+1, 0, len(runes)), true

	case tea.KeyHome:
		return value, 0, true

	case tea.KeyEnd:
		return value, len(runes), true
	}

	return value, cursor, false
}

func widthOf(runes []rune) int {
	w := 0
	for _, r := range runes {
		w += runewidth.RuneWidth(r)
	}
	return w
}

func clampInt(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
