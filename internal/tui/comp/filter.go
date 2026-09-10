package comp

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// Filter is a view's text filter.
//
// Filter is one of the three things a view is allowed to own outright, and
// every view that has one needs the same three behaviours: type into it, draw
// it, and ask whether a row survives it. Two views implementing those
// separately is how "/" comes to mean subtly different things on different
// screens, so they live here once.
//
// It is a value, like everything else a view holds, so a view updates its copy
// rather than mutating shared state.
type Filter struct {
	// Query is what has been typed. It keeps filtering after the input is
	// closed — that is the difference between a filter and a search box, and
	// it is why closing the bar does not clear the rows.
	Query string

	// Caret is the edit position, meaningful only while Active.
	Caret int

	// Active says the filter bar has the keyboard.
	Active bool
}

// Key applies a keystroke, reporting whether the filter consumed it.
//
// While the bar is open it consumes everything, because a filter you cannot
// type "j" into is not a filter. Enter accepts and closes, leaving the query
// in force; escape closes and clears, which is the only way back to the
// unfiltered list without deleting the text by hand.
func (f Filter) Key(msg tea.KeyMsg) (Filter, bool) {
	if !f.Active {
		return f, false
	}

	switch msg.Type {
	case tea.KeyEnter:
		f.Active = false
		return f, true
	case tea.KeyEsc:
		f.Active, f.Query, f.Caret = false, "", 0
		return f, true
	}

	if query, caret, handled := EditKey(f.Query, f.Caret, msg); handled {
		f.Query, f.Caret = query, caret
	}
	return f, true
}

// Open starts editing, keeping whatever is already there so "/" twice is a
// correction rather than a restart.
func (f Filter) Open() Filter {
	f.Active = true
	f.Caret = len([]rune(f.Query))
	return f
}

// On reports whether anything is being filtered out.
func (f Filter) On() bool { return f.Query != "" }

// Matches reports whether any of the given fields contains the query, folded
// to lower case.
//
// Substring rather than fuzzy: a fleet key that stops servers should not be
// pointed at a row by a match nobody can see the logic of.
func (f Filter) Matches(fields ...string) bool {
	if f.Query == "" {
		return true
	}
	needle := strings.ToLower(f.Query)
	for _, s := range fields {
		if strings.Contains(strings.ToLower(s), needle) {
			return true
		}
	}
	return false
}

// Render draws the filter bar, or nothing when it is neither open nor holding
// a query.
func (f Filter) Render(t *Theme, width int) string {
	if !f.Active && f.Query == "" {
		return ""
	}
	if width < 4 {
		return ""
	}

	label := "/"
	if !f.Active {
		// Closed but still filtering. Saying so is the whole point: rows are
		// missing and the reason should not be a mystery.
		text := "/" + f.Query + "  (esc clears)"
		return t.Dim.Render(Truncate(text, width))
	}
	return t.Accent.Render(label) + Input{
		Value: f.Query, Cursor: f.Caret, Width: width - Width(label), Theme: t,
	}.Render()
}
