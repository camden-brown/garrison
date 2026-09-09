package comp

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// A panel sits beside another panel. One that renders a cell wider than asked
// pushes its neighbour over, and the whole bottom half of the screen shears.
func TestPanelIsExactlyItsWidth(t *testing.T) {
	theme := NewTheme(false)

	cases := map[string]Panel{
		"plain":        {Theme: theme, Width: 40},
		"titled":       {Theme: theme, Title: "SERVERS", Width: 40},
		"with hint":    {Theme: theme, Title: "SERVERS", Right: "u start · S stop", Width: 40},
		"narrow":       {Theme: theme, Title: "ATTENTION", Right: "a ack", Width: 14},
		"fixed height": {Theme: theme, Title: "ACTIVITY", Width: 30, Height: 8},
		"ascii":        {Theme: NewTheme(true), Title: "SERVERS", Right: "keys", Width: 40},
		"focused":      {Theme: theme, Title: "SERVERS", Width: 40, Focused: true},
	}

	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			out := p.Render("body line\nsecond line")
			for i, line := range strings.Split(out, "\n") {
				if w := lipgloss.Width(line); w != p.Width {
					t.Errorf("line %d is %d cells, want %d: %q", i, w, p.Width, line)
				}
			}
		})
	}
}

func TestPanelHonoursItsHeight(t *testing.T) {
	p := Panel{Theme: NewTheme(false), Title: "X", Width: 20, Height: 6}
	if got := len(strings.Split(p.Render("a\nb"), "\n")); got != 6 {
		t.Errorf("rendered %d lines, want 6", got)
	}
}

// A long line must not push the border out and misalign everything beside it.
func TestPanelClampsOverlongContent(t *testing.T) {
	p := Panel{Theme: NewTheme(false), Title: "X", Width: 20}
	out := p.Render(strings.Repeat("x", 500))

	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w != 20 {
			t.Errorf("line is %d cells, want 20: %q", w, line)
		}
	}
}

// Too many rows for the box must be cut, not allowed to grow it.
func TestPanelClampsOverlongContentVertically(t *testing.T) {
	p := Panel{Theme: NewTheme(false), Title: "X", Width: 20, Height: 5}
	out := p.Render(strings.Repeat("line\n", 50))

	if got := len(strings.Split(out, "\n")); got != 5 {
		t.Errorf("rendered %d lines, want 5", got)
	}
}

// The heading is the first line inside the box, not a gap cut into the top
// border — a title spliced into a rule competes with the rule for the eye, and
// the corners stop looking like corners.
func TestTitleSitsInsideTheBox(t *testing.T) {
	lines := strings.Split(Panel{Theme: NewTheme(false), Title: "SERVERS", Right: "a ack", Width: 40}.Render("body"), "\n")

	if strings.Contains(lines[0], "SERVERS") {
		t.Errorf("the title is in the top border, not inside the box: %q", lines[0])
	}
	if !strings.Contains(lines[1], "SERVERS") {
		t.Errorf("first line inside is %q, want the title", lines[1])
	}
	if !strings.Contains(lines[1], "a ack") {
		t.Errorf("the hint is not on the heading line: %q", lines[1])
	}
	if !strings.Contains(lines[2], "body") {
		t.Errorf("the content does not follow the heading: %q", lines[2])
	}
}

// Only the focused panel's title is accented. If they all were, the accent
// would say nothing and the panel taking your keystrokes would not stand out.
func TestOnlyTheFocusedPanelAccentsItsTitle(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(termenv.Ascii)

	focused := Panel{Theme: NewTheme(false), Title: "SERVERS", Width: 30, Focused: true}.Render("x")
	quiet := Panel{Theme: NewTheme(false), Title: "SERVERS", Width: 30}.Render("x")

	if focused == quiet {
		t.Error("a focused panel renders identically to an unfocused one")
	}
}

// A title longer than the box must not make the box longer.
func TestOverlongTitleIsTruncated(t *testing.T) {
	p := Panel{Theme: NewTheme(false), Title: strings.Repeat("LONG ", 20), Right: "hint", Width: 16}
	for _, line := range strings.Split(p.Render("x"), "\n") {
		if w := lipgloss.Width(line); w != 16 {
			t.Errorf("line is %d cells, want 16: %q", w, line)
		}
	}
}

func TestTileStripFitsFourAcrossAtStageWidth(t *testing.T) {
	tiles := []Tile{{Label: "A", Value: "1"}, {Label: "B", Value: "2"}, {Label: "C", Value: "3"}, {Label: "D", Value: "4"}}

	// 92 is what the stage gets at 120 columns once the rail has taken its
	// share. All four must land on one row.
	out := TileStrip(NewTheme(false), 92, tiles)
	// Two border rows, the heading, the value, the sparkline and the note.
	if got := len(strings.Split(out, "\n")); got != 6 {
		t.Errorf("tile strip is %d lines, want 6 — the four tiles should be one row", got)
	}
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > 92 {
			t.Errorf("tile row is %d cells, want at most 92", w)
		}
	}
}
