package comp

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Panel is a titled box.
//
// The border is not decoration. Every pane in this interface is a different
// kind of thing — a fleet, an alert list, an activity feed — and at a glance
// the eye needs to know where one ends and the next begins. Without the boxes
// the screen reads as one undifferentiated column of text, which is how a
// dashboard becomes something you stop scanning and start ignoring.
type Panel struct {
	Theme *Theme
	Title string
	// Right is a hint shown at the right end of the title rule: the keys
	// that act on this panel, or what it is scoped to.
	Right string
	Width int
	// Height is the total including the border and the heading line. Zero
	// fits the content.
	Height int
	// Focused draws the border in the accent colour.
	Focused bool
}

// hPad is the breathing room inside a box, per side.
//
// Content flush against a border is harder to read than it looks: the eye has
// to separate the text from the rule, and at a glance a column of glyphs
// touching a vertical line reads as one shape.
const hPad = 1

// Inner is the usable width inside a panel of the given outer width — the box
// minus its borders and its padding. Callers laying out columns need the same
// number the panel will use, and computing it in two places is how they drift.
func Inner(width int) int {
	inner := width - 2 - 2*hPad
	if inner < 1 {
		inner = 1
	}
	return inner
}

// Render wraps body in the box.
func (p Panel) Render(body string) string {
	inner := Inner(p.Width)

	border := lipgloss.RoundedBorder()
	if p.Theme.ASCII {
		border = lipgloss.Border{
			Top: "-", Bottom: "-", Left: "|", Right: "|",
			TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+",
		}
	}

	// The heading is the first line inside the box, not a gap cut into the
	// top border. Both are common in terminal interfaces; this one is what
	// the design uses, and it reads better at small sizes — a title spliced
	// into a rule competes with the rule for the eye, and the corners stop
	// looking like corners.
	content := body
	if head := p.heading(inner); head != "" {
		content = head + "\n" + body
	}

	style := lipgloss.NewStyle().
		Border(border).
		BorderForeground(borderColour(p.Theme, p.Focused)).
		Padding(0, hPad).
		// lipgloss counts padding inside Width, so this is the box minus
		// its borders — not minus the padding as well, which would shrink
		// the whole panel by two cells and pull its neighbour left.
		Width(p.Width - 2)

	if p.Height > 2 {
		style = style.Height(p.Height - 2)
	}
	return style.Render(clampBody(content, inner, p.Height))
}

// heading is the title line: the name at the left, the hint at the right.
func (p Panel) heading(inner int) string {
	if p.Title == "" && p.Right == "" {
		return ""
	}

	title, right := p.Title, p.Right
	if Width(title)+Width(right)+1 > inner {
		right = ""
		title = Truncate(title, inner)
	}

	gap := inner - Width(title) - Width(right)
	if gap < 0 {
		gap = 0
	}
	// Only the focused panel's title is accented. If they all were, the
	// accent would say nothing, and the one that is actually taking your
	// keystrokes would not stand out.
	name := p.Theme.Header
	if p.Focused {
		name = p.Theme.Accent
	}
	return name.Render(title) +
		strings.Repeat(" ", gap) +
		p.Theme.Dim.Render(right)
}

func borderColour(t *Theme, focused bool) lipgloss.TerminalColor {
	if focused {
		return colAmber
	}
	return colGrey
}

// clampBody trims content to the box so a long line cannot push the border
// out and misalign everything beside it.
func clampBody(body string, width, height int) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if Width(line) > width {
			lines[i] = Truncate(line, width)
		}
	}
	if height > 2 && len(lines) > height-2 {
		lines = lines[:height-2]
	}
	return strings.Join(lines, "\n")
}

// Columns lays panels side by side with a one-column gap, which is how the
// bottom half of the fleet screen is built.
func Columns(gap int, blocks ...string) string {
	parts := make([]string, 0, len(blocks)*2)
	for i, b := range blocks {
		if i > 0 {
			parts = append(parts, strings.Repeat(" ", gap))
		}
		parts = append(parts, b)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}
