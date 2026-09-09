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
	// Height is the total including the border. Zero fits the content.
	Height int
	// Focused draws the border in the accent colour.
	Focused bool
}

// Render wraps body in the box.
func (p Panel) Render(body string) string {
	inner := p.Width - 2
	if inner < 1 {
		inner = 1
	}

	border := lipgloss.RoundedBorder()
	if p.Theme.ASCII {
		border = lipgloss.Border{
			Top: "-", Bottom: "-", Left: "|", Right: "|",
			TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+",
		}
	}

	// The top edge is drawn here rather than by lipgloss, because the title
	// has to sit inside it. Asking lipgloss for it and then editing the
	// result does not work: with colour enabled the rendered border is
	// wrapped in escape sequences, so indexing it for the corner runes
	// picks up an escape byte instead and the line comes out a cell short.
	style := lipgloss.NewStyle().
		Border(border, false, true, true, true).
		BorderForeground(borderColour(p.Theme, p.Focused)).
		Width(inner)

	if p.Height > 2 {
		style = style.Height(p.Height - 2)
	}

	boxed := style.Render(clampBody(body, inner, p.Height))
	return p.top(border, inner) + "\n" + boxed
}

// top is the heading rule: corners, the title, and the hint.
func (p Panel) top(border lipgloss.Border, inner int) string {
	edge := lipgloss.NewStyle().Foreground(borderColour(p.Theme, p.Focused))

	title, right := "", ""
	if p.Title != "" {
		title = " " + p.Title + " "
	}
	if p.Right != "" {
		right = " " + p.Right + " "
	}

	// When the labels do not fit, the hint goes first and the title is
	// trimmed to what is left. The gap is recomputed either way: a rule that
	// stops short leaves the box a cell narrow, and a box a cell narrow
	// shoves its neighbour sideways.
	if Width(title)+Width(right) > inner {
		right = ""
		title = Truncate(title, inner)
	}
	gap := inner - Width(title) - Width(right)
	if gap < 0 {
		gap = 0
	}

	return edge.Render(border.TopLeft) +
		p.Theme.Title.Render(title) +
		edge.Render(strings.Repeat(border.Top, gap)) +
		p.Theme.Dim.Render(right) +
		edge.Render(border.TopRight)
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
