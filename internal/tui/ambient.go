package tui

import (
	"fmt"
	"strings"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tui/comp"
)

// Ambient mode is the window you never close.
//
// It is not a view, which is the point: DESIGN gives it no rail, no status bar
// and no keys except the one that leaves, so putting it behind the View
// contract would mean a screen that has to opt out of everything the contract
// provides. It is a second way for the shell to draw the same snapshot.
//
// What it drops is chrome. What it keeps is the three things you would glance
// across a room for: whether each server is up, how loaded it is, and whether
// anybody is on.

// ambientRefresh is how often the shell redraws in ambient mode.
//
// DESIGN says five seconds. The store still publishes on every mutation — the
// snapshot is as live as ever — but a card that is legible from the other side
// of a room does not need to flicker at one hertz, and a terminal that repaints
// constantly is a terminal you notice.
const ambientRefresh = 5

// ambient renders one card per server, filling the terminal.
func (a *App) ambientView() string {
	t := a.theme

	if len(a.snap.Servers) == 0 {
		return "\n" + t.Dim.Render("  No servers. Any key leaves ambient mode.")
	}

	// Two columns when there is room for two readable cards, one otherwise.
	// A card narrower than about forty columns loses the sparkline, which is
	// most of what the mode is for.
	perRow := 1
	if a.width >= 84 {
		perRow = 2
	}
	cardWidth := (a.width - (perRow-1)*2) / perRow

	cards := make([]string, 0, len(a.snap.Servers))
	for _, srv := range a.snap.Servers {
		cards = append(cards, a.ambientCard(srv, cardWidth))
	}

	var b strings.Builder
	for i := 0; i < len(cards); i += perRow {
		end := i + perRow
		if end > len(cards) {
			end = len(cards)
		}
		b.WriteString(joinRow(cards[i:end], "  "))
		b.WriteString("\n")
	}

	b.WriteString(t.Dim.Render(fmt.Sprintf("  %d servers · refreshing every %ds · any key leaves",
		len(a.snap.Servers), ambientRefresh)))
	return strings.TrimRight(b.String(), "\n")
}

// ambientCard is one server, large.
func (a *App) ambientCard(srv core.Server, width int) string {
	t := a.theme
	inner := comp.Inner(width)

	// The state is a word, a glyph and a colour, the same three ways it is
	// encoded everywhere else — a card read from across a room is exactly
	// where a colour-only encoding fails.
	state := t.StateStyle(srv.State).Render(t.StateGlyph(srv.State) + " " + strings.ToUpper(t.StateWord(srv.State)))

	var rows []string
	rows = append(rows, state)

	if srv.Detail != "" {
		rows = append(rows, t.Dim.Render(comp.Truncate(srv.Detail, inner)))
	}

	if srv.State == model.StateRunning {
		spark := comp.Sparkline{Width: inner, ASCII: t.ASCII}.Render(srv.CPU.Hot)
		rows = append(rows, t.Spark.Render(spark))

		figures := []string{}
		if last, ok := srv.CPU.Last(); ok {
			figures = append(figures, fmt.Sprintf("cpu %.0f%%", last.Mean))
		}
		if last, ok := srv.Mem.Last(); ok {
			if srv.MemLimit > 0 {
				figures = append(figures, fmt.Sprintf("mem %s / %s",
					comp.Bytes(int64(last.Mean)), comp.Bytes(srv.MemLimit)))
			} else {
				figures = append(figures, "mem "+comp.Bytes(int64(last.Mean)))
			}
		}
		figures = append(figures, fmt.Sprintf("%d online", len(srv.Players)))
		if !srv.Started.IsZero() {
			figures = append(figures, "up "+comp.Duration(a.Now().Sub(srv.Started)))
		}
		rows = append(rows, t.Dim.Render(comp.Truncate(strings.Join(figures, " · "), inner)))
	}

	if srv.Busy != core.OpNone {
		rows = append(rows, t.Accent.Render(comp.Truncate(srv.Busy.Present()+"…", inner)))
	}

	return comp.Panel{Theme: t, Title: strings.ToUpper(srv.Name), Width: width}.
		Render(strings.Join(rows, "\n"))
}

// joinRow puts cards side by side, padding the shorter ones so the row has a
// flat bottom rather than a ragged one.
func joinRow(cards []string, gap string) string {
	if len(cards) == 1 {
		return cards[0]
	}

	split := make([][]string, len(cards))
	height := 0
	for i, c := range cards {
		split[i] = strings.Split(c, "\n")
		if len(split[i]) > height {
			height = len(split[i])
		}
	}

	var b strings.Builder
	for row := 0; row < height; row++ {
		parts := make([]string, 0, len(split))
		for _, lines := range split {
			if row < len(lines) {
				parts = append(parts, lines[row])
				continue
			}
			parts = append(parts, strings.Repeat(" ", comp.Width(lines[0])))
		}
		b.WriteString(strings.TrimRight(strings.Join(parts, gap), " "))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}
