// Package players is the Players view: who is on now, who has been on, and
// when the server is usually empty.
//
// The last of those is the one that earns the screen. Picking a restart window
// out of the air annoys somebody eventually; picking it out of a week of
// occupancy does not, and the whole point of keeping session history is to
// make that a look rather than a guess.
package players

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tui"
	"github.com/camden-brown/garrison/internal/tui/comp"
)

// View shows the roster, the history and the occupancy chart.
type View struct {
	// scroll is how far down the session list the window has moved. The
	// roster and the chart are fixed height, so it is the only cursor here.
	scroll int
}

func New() *View { return &View{} }

func (v *View) ID() tui.ViewID { return tui.ViewPlayers }
func (v *View) Title() string  { return "Players" }

// Available refuses without a server, because a roster belongs to one.
func (v *View) Available(inst model.Instance) (bool, string) {
	if inst.Name == "" {
		return false, "Pick a server first — a roster belongs to one."
	}
	return true, ""
}

var (
	keyUp   = key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up"))
	keyDown = key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down"))
)

func (v *View) Keys() []key.Binding { return []key.Binding{keyUp, keyDown} }

func (v *View) Update(msg tea.Msg, f tui.Frame, snap core.Snapshot) (tui.View, tea.Cmd) {
	msgKey, ok := msg.(tea.KeyMsg)
	if !ok {
		return v, nil
	}
	srv, ok := snap.Server(f.Server)
	if !ok {
		return v, nil
	}

	next := *v
	switch {
	case key.Matches(msgKey, keyUp):
		next.scroll--
	case key.Matches(msgKey, keyDown):
		next.scroll++
	}
	next.clamp(len(srv.Sessions), sessionRows(f))
	return &next, nil
}

func (v *View) clamp(total, page int) {
	if top := total - page; v.scroll > top {
		v.scroll = top
	}
	if v.scroll < 0 {
		v.scroll = 0
	}
}

// sessionRows is what is left for the history after the roster and the chart
// have taken their fixed share.
func sessionRows(f tui.Frame) int {
	n := f.Height - 14
	if n < 1 {
		return 1
	}
	return n
}

func (v *View) Render(f tui.Frame, snap core.Snapshot) string {
	t := f.Theme

	srv, ok := snap.Server(f.Server)
	if !ok {
		return t.Dim.Render("No such server.")
	}
	v.clamp(len(srv.Sessions), sessionRows(f))

	now := comp.Panel{
		Theme: t, Title: "ONLINE NOW", Right: srv.Name,
		Width: f.Width, Focused: f.Focused,
	}.Render(v.roster(f, srv))

	occupancy := comp.Panel{
		Theme: t, Title: "OCCUPANCY BY HOUR", Right: "7 days · local time",
		Width: f.Width,
	}.Render(v.occupancy(f, srv))

	history := comp.Panel{
		Theme: t, Title: "SESSIONS", Right: v.historyRight(srv),
		Width: f.Width,
	}.Render(v.sessions(f, srv))

	return now + "\n" + occupancy + "\n" + history
}

func (v *View) roster(f tui.Frame, srv core.Server) string {
	t := f.Theme

	if len(srv.Players) == 0 {
		if srv.State != model.StateRunning {
			return t.Dim.Render("Nobody — the server is not running.")
		}
		return t.Dim.Render("Nobody connected.")
	}

	var b strings.Builder
	for _, p := range srv.Players {
		name := p.Name
		if name == "" {
			// A connection Garrison has seen but cannot name yet. Saying so
			// beats an empty row, and it is the visible face of the roster
			// pairing debt.
			name = "(connecting)"
		}
		b.WriteString(t.Title.Render(comp.Pad(name, 24)))
		if !p.Since.IsZero() {
			b.WriteString(t.Dim.Render(comp.Pad("on for "+comp.Duration(f.Now.Sub(p.Since)), 18)))
		} else {
			b.WriteString(t.Dim.Render(comp.Pad("", 18)))
		}
		if p.SteamID != "" {
			b.WriteString(t.Dim.Render(p.SteamID))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// occupancy renders average concurrent players for each hour of the day.
//
// Averaged over the days in the window rather than summed, so the number is
// "how many people are usually on at 9pm" — which is the question a restart
// window is chosen against. An hour with no data reads as zero, which is
// correct: nobody was on.
func (v *View) occupancy(f tui.Frame, srv core.Server) string {
	t := f.Theme

	if len(srv.Sessions) == 0 {
		return t.Dim.Render("No history yet. It builds as people play.")
	}

	hours := HourlyOccupancy(srv.Sessions, f.Now)

	// One cell per hour, plus a label row that lines the numbers up under
	// the bars they belong to.
	points := make([]model.Point, 0, len(hours))
	var peak float64
	for _, h := range hours {
		points = append(points, model.Point{Mean: h})
		if h > peak {
			peak = h
		}
	}

	line := comp.Sparkline{Width: len(points), ASCII: t.ASCII, Min: 0, Max: peak}.Render(points)

	// Ticks every six hours, one cell per hour so the numbers sit under the
	// bars they describe. A label under all twenty-four would not fit and
	// would not be read.
	labels := "0     6     12    18    "

	quiet, busy := quietestHour(hours), busiestHour(hours)
	summary := fmt.Sprintf("quietest %02d:00 · busiest %02d:00 · peak %.1f", quiet, busy, peak)

	return t.Spark.Render(line) + "\n" + t.Dim.Render(labels) + "\n" + t.Dim.Render(summary)
}

func (v *View) sessions(f tui.Frame, srv core.Server) string {
	t := f.Theme

	if len(srv.Sessions) == 0 {
		return t.Dim.Render("Nothing recorded yet.")
	}

	page := sessionRows(f)
	start := v.scroll
	end := start + page
	if end > len(srv.Sessions) {
		end = len(srv.Sessions)
	}

	var b strings.Builder
	for _, s := range srv.Sessions[start:end] {
		name := s.Player
		if name == "" {
			name = "(unnamed)"
		}

		when := comp.Stamp(s.Joined, f.Now)
		length := comp.Duration(s.Duration(f.Now))
		if s.Open() {
			length += " (on now)"
		}

		b.WriteString(t.Title.Render(comp.Pad(name, 24)))
		b.WriteString(t.Dim.Render(comp.Pad(when, comp.StampWidth+2)))
		b.WriteString(t.Dim.Render(comp.Pad(length, 18)))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (v *View) historyRight(srv core.Server) string {
	if len(srv.Sessions) == 0 {
		return ""
	}
	names := map[string]bool{}
	for _, s := range srv.Sessions {
		names[s.Player] = true
	}
	return fmt.Sprintf("%d sessions · %d people · 7 days", len(srv.Sessions), len(names))
}

// HourlyOccupancy is the average number of players connected during each hour
// of the day, across the window the sessions cover.
//
// Exported because it is the only real arithmetic on this screen, and a
// number that decides when a server restarts is worth testing directly rather
// than through a rendered string.
func HourlyOccupancy(sessions []model.Session, now time.Time) [24]float64 {
	var minutes [24]float64

	for _, s := range sessions {
		end := s.Left
		if s.Open() {
			end = now
		}
		if !end.After(s.Joined) {
			continue
		}

		// Walk the session a minute at a time. Sessions are hours long and
		// the window is a week, so this is thousands of iterations on a
		// screen that redraws a few times a second — and it is exact at the
		// hour boundaries, which an analytic split gets wrong on daylight
		// saving.
		for at := s.Joined.Local(); at.Before(end.Local()); at = at.Add(time.Minute) {
			minutes[at.Hour()]++
		}
	}

	// Average over the days the window actually covers, so a fresh install
	// with one day of history is not reported as a seventh as busy as it is.
	days := coveredDays(sessions, now)
	var out [24]float64
	for h := range minutes {
		out[h] = minutes[h] / 60 / days
	}
	return out
}

// coveredDays is how many days of history there are, never less than one.
func coveredDays(sessions []model.Session, now time.Time) float64 {
	oldest := now
	for _, s := range sessions {
		if s.Joined.Before(oldest) {
			oldest = s.Joined
		}
	}
	days := now.Sub(oldest).Hours() / 24
	if days < 1 {
		return 1
	}
	return days
}

func quietestHour(hours [24]float64) int {
	at, lowest := 0, hours[0]
	for h, v := range hours {
		if v < lowest {
			at, lowest = h, v
		}
	}
	return at
}

func busiestHour(hours [24]float64) int {
	at, highest := 0, hours[0]
	for h, v := range hours {
		if v > highest {
			at, highest = h, v
		}
	}
	return at
}

// Capturing is false: nothing here takes text.
func (v *View) Capturing() bool { return false }
