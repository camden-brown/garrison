package comp

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
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

// The title goes into the top border. Slicing that rule by byte would cut a
// multibyte corner rune in half.
func TestTitleIsWrittenIntoTheBorder(t *testing.T) {
	out := Panel{Theme: NewTheme(false), Title: "SERVERS", Right: "a ack", Width: 40}.Render("body")
	first := strings.Split(out, "\n")[0]

	if !strings.Contains(first, "SERVERS") {
		t.Errorf("title is not in the top border: %q", first)
	}
	if !strings.Contains(first, "a ack") {
		t.Errorf("hint is not in the top border: %q", first)
	}
	if strings.Contains(first, "�") {
		t.Errorf("the border rule was cut mid-rune: %q", first)
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
	if got := len(strings.Split(out, "\n")); got != 5 {
		t.Errorf("tile strip is %d lines, want 5 — the four tiles should be one row", got)
	}
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > 92 {
			t.Errorf("tile row is %d cells, want at most 92", w)
		}
	}
}
