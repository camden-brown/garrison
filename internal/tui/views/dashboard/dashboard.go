// Package dashboard is the per-server screen: four tiles, and the log behind
// them.
//
// The fourth tile is game-supplied. A plugin's Parse emits a named metric and
// the tile renders whatever comes out, so Zomboid can show zombies alive and
// Valheim world-save duration with no per-game code here. That indirection is
// the whole reason the tile exists in this shape.
package dashboard

import (
	"fmt"
	"strconv"
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
	i := indexOf(snap, from)
	i += delta
	if i < 0 {
		i = 0
	}
	if i >= len(snap.Servers) {
		i = len(snap.Servers) - 1
	}
	return snap.Servers[i].Name
}

func indexOf(snap core.Snapshot, name string) int {
	for i, srv := range snap.Servers {
		if srv.Name == name {
			return i
		}
	}
	return 0
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
	b.WriteString("\n")

	// Under about 20 rows the tile strip collapses to one line of values.
	// A real narrow layout, not a clipped wide one.
	if f.Height >= 20 {
		b.WriteString(comp.TileStrip(t, f.Width, tiles(srv)))
	} else {
		b.WriteString(comp.InlineTiles(t, f.Width, tiles(srv)))
	}
	b.WriteString("\n")

	if tail := logTail(f, srv); tail != "" {
		b.WriteString(tail)
	}
	return b.String()
}

func tiles(srv core.Server) []comp.Tile {
	return []comp.Tile{cpuTile(srv), memTile(srv), playersTile(srv), gameTile(srv)}
}

func cpuTile(srv core.Server) comp.Tile {
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
	return comp.Tile{Label: "CPU", Value: value, Points: srv.CPU.Hot, Min: 0, Max: 100}
}

func memTile(srv core.Server) comp.Tile {
	value, right := "—", ""
	if last, ok := srv.Mem.Last(); ok {
		value = comp.Bytes(int64(last.Mean))
	}
	if srv.MemLimit > 0 {
		right = comp.Bytes(srv.MemLimit)
	}
	return comp.Tile{
		Label:  "MEM",
		Right:  right,
		Value:  value,
		Points: srv.Mem.Hot,
		Max:    float64(srv.MemLimit),
	}
}

// playersTile counts the roster reconstructed from log events.
//
// A stopped server shows a dash rather than zero: "nobody is playing" and
// "there is nothing running to play on" are different facts, and a dashboard
// that renders them identically is one you stop trusting.
func playersTile(srv core.Server) comp.Tile {
	if !srv.State.Live() {
		return comp.Tile{Label: "PLAYERS", Value: "—"}
	}

	note := ""
	if len(srv.Players) > 0 {
		note = strings.Join(playerNames(srv.Players), ", ")
	}
	return comp.Tile{Label: "PLAYERS", Value: strconv.Itoa(len(srv.Players)), Note: note}
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
func gameTile(srv core.Server) comp.Tile {
	// A game that supplies no metric gets network throughput instead, which
	// is the documented fallback and is always available.
	if srv.MetricLabel == "" {
		value := "—"
		if last, ok := srv.Net.Last(); ok {
			value = comp.Bytes(int64(last.Mean)) + "/s"
		}
		return comp.Tile{Label: "NET I/O", Value: value, Points: srv.Net.Hot}
	}

	value := "—"
	if last, ok := srv.GameMetric.Last(); ok {
		value = fmt.Sprintf("%.0f", last.Mean)
	}
	return comp.Tile{
		Label:  strings.ToUpper(srv.MetricLabel),
		Value:  value,
		Points: srv.GameMetric.Hot,
	}
}

// logTail is the last few classified lines, newest at the bottom.
//
// Repeated identical lines collapse into a counter, because a crash-looping
// mod otherwise erases the last hour of history in seconds — the tail would
// show nothing but the same line, and the thing that caused it would be gone.
func logTail(f tui.Frame, srv core.Server) string {
	rows := f.Height - 10
	if rows < 3 || srv.Console.Len() == 0 {
		return ""
	}

	// Collapse a window several times the visible height rather than just
	// the rows about to be shown: a flood collapses to one counter, so a
	// window of exactly `rows` would show that counter and nothing of what
	// came before it. The bound is what keeps a sixteen-thousand-line ring
	// from being walked on every frame.
	const window = 40
	collapsed := comp.CollapseRepeats(srv.Console.Tail(rows * window))
	if len(collapsed) > rows {
		collapsed = collapsed[len(collapsed)-rows:]
	}

	t := f.Theme
	var b strings.Builder
	for _, line := range collapsed {
		text := line.Text()
		if line.Count > 1 {
			text += fmt.Sprintf("  %d×", line.Count)
		}
		b.WriteString(t.Dim.Render(comp.Truncate(text, comp.Inner(f.Width))))
		b.WriteString("\n")
	}

	return comp.Panel{
		Theme:   t,
		Title:   "CONSOLE",
		Right:   srv.Name,
		Width:   f.Width,
		Focused: f.Focused,
	}.Render(strings.TrimRight(b.String(), "\n"))
}
