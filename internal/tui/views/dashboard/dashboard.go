// Package dashboard is the per-server screen: four tiles, and the numbers
// behind them.
//
// The fourth tile is game-supplied. A plugin's Parse emits a named metric and
// the tile renders whatever comes out, so Zomboid can show zombies alive and
// Valheim world-save duration with no per-game code here. That indirection is
// the whole reason the tile exists in this shape.
package dashboard

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tui"
	"github.com/camden-brown/garrison/internal/tui/comp"
)

// View renders one server.
//
// It owns nothing. Which server it is about arrives on the Frame, because the
// rail and the stage are looking at the same selection.
type View struct{}

func New() *View { return &View{} }

func (v *View) ID() tui.ViewID { return tui.ViewDashboard }
func (v *View) Title() string  { return "Dashboard" }

// Available: the dashboard works for any game, because everything on it comes
// from the container or from Parse.
func (v *View) Available(model.Instance) (bool, string) { return true, "" }

var (
	keyPrev = key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "previous server"))
	keyNext = key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "next server"))
)

func (v *View) Keys() []key.Binding { return []key.Binding{keyPrev, keyNext} }

func (v *View) Update(msg tea.Msg, f tui.Frame, snap core.Snapshot) (tui.View, tea.Cmd) {
	msgKey, ok := msg.(tea.KeyMsg)
	if !ok {
		return v, nil
	}

	switch {
	case key.Matches(msgKey, keyPrev):
		return v, tui.Select(step(snap, f.Server, -1))
	case key.Matches(msgKey, keyNext):
		return v, tui.Select(step(snap, f.Server, +1))
	}
	return v, nil
}

// step is the server delta places from the named one, clamped.
func step(snap core.Snapshot, from string, delta int) string {
	if len(snap.Servers) == 0 {
		return ""
	}
	i := 0
	for n, srv := range snap.Servers {
		if srv.Name == from {
			i = n
			break
		}
	}
	i += delta
	if i < 0 {
		i = 0
	}
	if i >= len(snap.Servers) {
		i = len(snap.Servers) - 1
	}
	return snap.Servers[i].Name
}

func (v *View) Render(f tui.Frame, snap core.Snapshot) string {
	t := f.Theme

	if len(snap.Servers) == 0 {
		return t.Dim.Render("No servers to show.") + "\n"
	}

	srv, ok := snap.Server(f.Server)
	if !ok {
		srv = snap.Servers[0]
	}

	var b strings.Builder
	b.WriteString(t.Title.Render(srv.Name))
	b.WriteString("  ")
	b.WriteString(t.StateStyle(srv.State).Render(t.StateGlyph(srv.State) + " " + t.StateWord(srv.State)))
	if n := len(snap.Servers); n > 1 {
		b.WriteString("  ")
		b.WriteString(t.Dim.Render(fmt.Sprintf("%d/%d · ←→ to move", indexOf(snap, srv.Name)+1, n)))
	}
	b.WriteString("\n\n")

	b.WriteString(tiles(f, srv))

	if tail := logTail(f, srv); tail != "" {
		b.WriteString(tail)
	}
	return b.String()
}

// logTail is the last few classified lines, newest at the bottom.
//
// Repeated identical lines collapse into a counter, because a crash-looping
// mod otherwise erases the last hour of history in seconds — the tail would
// show nothing but the same line, and the thing that caused it would be gone.
func logTail(f tui.Frame, srv core.Server) string {
	rows := f.Height - 12
	if rows < 3 || len(srv.Console) == 0 {
		return ""
	}

	collapsed := collapse(srv.Console)
	if len(collapsed) > rows {
		collapsed = collapsed[len(collapsed)-rows:]
	}

	t := f.Theme
	var b strings.Builder
	b.WriteString(t.Header.Render("CONSOLE"))
	b.WriteString("\n")
	for _, line := range collapsed {
		text := line.text
		if line.count > 1 {
			text += fmt.Sprintf("  %d×", line.count)
		}
		b.WriteString(t.Dim.Render(comp.Truncate(text, f.Width)))
		b.WriteString("\n")
	}
	return b.String()
}

type collapsedLine struct {
	text  string
	count int
}

func collapse(events []model.Event) []collapsedLine {
	out := make([]collapsedLine, 0, len(events))
	for _, ev := range events {
		text := ev.Text
		if text == "" {
			text = ev.Raw
		}
		if strings.TrimSpace(text) == "" {
			// An event with nothing to say — a bare connection, say — is
			// still a fact for the roster but not a console line.
			continue
		}
		if n := len(out); n > 0 && out[n-1].text == text {
			out[n-1].count++
			continue
		}
		out = append(out, collapsedLine{text: text, count: 1})
	}
	return out
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func indexOf(snap core.Snapshot, name string) int {
	for i, srv := range snap.Servers {
		if srv.Name == name {
			return i
		}
	}
	return 0
}

// tileWidth is fixed rather than proportional so the tiles line up with each
// other and with the fleet table at any terminal width.
//
// Sized so four fit beside the rail at 120 columns, which is the layout the
// design is drawn at: 4×21 plus three two-column gaps is 90, inside the 92 the
// stage gets once the rail has taken its 26.
const tileWidth = 21

// tiles draws the strip. Under 30 rows the design collapses it to one line of
// inline values; that is the narrow layout, not a clipped wide one.
func tiles(f tui.Frame, srv core.Server) string {
	specs := []tileSpec{
		cpuTile(srv),
		memTile(srv),
		playersTile(srv),
		gameTile(srv),
	}

	perRow := f.Width / (tileWidth + 2)
	if perRow < 1 {
		perRow = 1
	}
	if f.Height < 12 {
		return inline(f, specs)
	}

	var b strings.Builder
	for start := 0; start < len(specs); start += perRow {
		end := start + perRow
		if end > len(specs) {
			end = len(specs)
		}
		b.WriteString(row(f, specs[start:end]))
	}
	return b.String()
}

type tileSpec struct {
	label  string
	value  string
	note   string
	points []model.Point

	// min and max pin the sparkline's scale. Left zero, it scales to the
	// data and shows shape instead of magnitude — right for a series with
	// no natural ceiling, wrong for one that has a real one.
	min, max float64
}

func cpuTile(srv core.Server) tileSpec {
	value := "—"
	if last, ok := srv.CPU.Last(); ok {
		value = fmt.Sprintf("%.1f%%", last.Mean)
	}
	// CPU has a natural scale, so it gets one rather than being drawn
	// against whatever its own maximum happened to be — otherwise a single
	// spike flattens an hour of ordinary variation into a bottom line, and
	// the same shape means something different on every server.
	//
	// A container may exceed 100% across several cores. Those clamp to full
	// height, which is the right signal: sustained triple-digit CPU on a
	// game server is a thing to look at, not a thing to scale away.
	return tileSpec{label: "CPU", value: value, points: srv.CPU.Hot, min: 0, max: 100}
}

func memTile(srv core.Server) tileSpec {
	value, note := "—", ""
	if last, ok := srv.Mem.Last(); ok {
		value = bytesLabel(int64(last.Mean))
	}
	if srv.MemLimit > 0 {
		note = "of " + bytesLabel(srv.MemLimit)
	}
	return tileSpec{
		label:  "MEM",
		value:  value,
		note:   note,
		points: srv.Mem.Hot,
		max:    float64(srv.MemLimit),
	}
}

// playersTile counts the roster reconstructed from log events.
//
// A stopped server shows a dash rather than zero: "nobody is playing" and
// "there is nothing running to play on" are different facts, and a dashboard
// that renders them identically is one you stop trusting.
func playersTile(srv core.Server) tileSpec {
	if !srv.State.Live() {
		return tileSpec{label: "PLAYERS", value: "—"}
	}

	note := ""
	if len(srv.Players) > 0 {
		note = strings.Join(playerNames(srv.Players), ", ")
	}
	return tileSpec{label: "PLAYERS", value: itoa(len(srv.Players)), note: note}
}

func playerNames(players []model.Player) []string {
	out := make([]string, 0, len(players))
	for _, p := range players {
		out = append(out, p.Name)
	}
	return out
}

// gameTile is whatever the plugin's Parse emits. There is no game-specific
// code here — the label and the value both come from the snapshot.
func gameTile(srv core.Server) tileSpec {
	// A game that supplies no metric gets network throughput instead, which
	// is the documented fallback and is always available.
	if srv.MetricLabel == "" {
		value := "—"
		if last, ok := srv.Net.Last(); ok {
			value = bytesLabel(int64(last.Mean)) + "/s"
		}
		return tileSpec{label: "NET I/O", value: value, points: srv.Net.Hot}
	}
	value := "—"
	if last, ok := srv.GameMetric.Last(); ok {
		value = fmt.Sprintf("%.0f", last.Mean)
	}
	return tileSpec{
		label:  strings.ToUpper(srv.MetricLabel),
		value:  value,
		points: srv.GameMetric.Hot,
	}
}

func row(f tui.Frame, specs []tileSpec) string {
	lines := make([][]string, len(specs))
	for i, s := range specs {
		lines[i] = tile(f, s)
	}

	var b strings.Builder
	for line := 0; line < 4; line++ {
		cells := make([]string, len(specs))
		for i := range specs {
			cells[i] = lines[i][line]
		}
		b.WriteString(strings.TrimRight(strings.Join(cells, "  "), " "))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	return b.String()
}

// tile is four lines: a heading, the number, a sparkline, and a note.
func tile(f tui.Frame, s tileSpec) []string {
	t := f.Theme
	inner := tileWidth - 2

	spark := comp.Sparkline{Width: inner, ASCII: t.ASCII, Min: s.min, Max: s.max}.Render(s.points)

	return []string{
		t.Header.Render(comp.Pad(s.label, tileWidth)),
		t.Title.Render(comp.Pad(s.value, tileWidth)),
		t.Accent.Render(comp.Pad(spark, tileWidth)),
		t.Dim.Render(comp.Pad(s.note, tileWidth)),
	}
}

// inline is the under-30-rows layout: one line of values, no sparklines.
func inline(f tui.Frame, specs []tileSpec) string {
	parts := make([]string, 0, len(specs))
	for _, s := range specs {
		parts = append(parts, f.Theme.Header.Render(s.label)+" "+f.Theme.Title.Render(s.value))
	}
	return comp.Truncate(strings.Join(parts, "  ·  "), f.Width) + "\n"
}

// bytesLabel renders a byte count the way an operator reads one.
func bytesLabel(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}
