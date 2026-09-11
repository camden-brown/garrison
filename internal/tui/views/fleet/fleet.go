// Package fleet is the Fleet view: every server on one line, and the three
// questions you ask about a fleet without meaning to.
//
// It is the default screen and the one left on the monitor, so it answers "is
// anything wrong" without scrolling, and offers the two verbs that fix the
// usual answer.
package fleet

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/model"
	engine "github.com/camden-brown/garrison/internal/tasks"
	"github.com/camden-brown/garrison/internal/tui"
	"github.com/camden-brown/garrison/internal/tui/comp"
)

// View lists the fleet.
//
// It owns only a pending confirmation. Which row is highlighted is the shell's
// selection, arriving on the Frame: the rail and this table show the same
// choice, and two cursors that can disagree about which server you are looking
// at is a bug waiting for a busy evening.
type View struct {
	confirm   string // instance awaiting confirmation, empty when none
	confirmOp core.Op

	// filter narrows the table. It is one of the three things a view owns,
	// and it stays in force after the bar is closed — the bar reports that
	// so missing rows never look like missing servers.
	filter comp.Filter

	// snap is the snapshot being drawn, held only for the length of a
	// render so row helpers can reach the task list without every one of
	// them taking it as an argument.
	snap core.Snapshot
}

// New returns the Fleet view.
func New() *View { return &View{} }

func (v *View) ID() tui.ViewID { return tui.ViewFleet }
func (v *View) Title() string  { return "Fleet" }

// Available: the fleet is never unavailable — it is the screen that explains
// why everything else is.
func (v *View) Available(model.Instance) (bool, string) { return true, "" }

var (
	keyUp      = key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up"))
	keyDown    = key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down"))
	keyStart   = key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "start"))
	keyStop    = key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "stop"))
	keyRestart = key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "restart"))
	keyBackup  = key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "backup"))
	keyUpdate  = key.NewBinding(key.WithKeys("U"), key.WithHelp("U", "update"))
	keyConfirm = key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "confirm"))
	keyCancel  = key.NewBinding(key.WithKeys("n", "esc"), key.WithHelp("n/esc", "cancel"))
)

func (v *View) Keys() []key.Binding {
	return []key.Binding{keyUp, keyDown, keyStart, keyStop, keyRestart, keyBackup, keyUpdate}
}

func (v *View) Update(msg tea.Msg, f tui.Frame, snap core.Snapshot) (tui.View, tea.Cmd) {
	msgKey, ok := msg.(tea.KeyMsg)
	if !ok {
		return v, nil
	}
	next := *v
	target := selectedOr(f.Server, snap)

	// The filter bar swallows every key while it is open, or typing a
	// server's name presses S on the "s" and stops one.
	if filter, handled := next.filter.Key(msgKey); handled {
		next.filter = filter
		return &next, nil
	}
	if msgKey.String() == "/" && next.confirm == "" {
		next.filter = next.filter.Open()
		return &next, nil
	}

	// A pending confirmation swallows every other key. Answering it is the
	// only thing the view will do until it is answered.
	if next.confirm != "" {
		switch {
		case key.Matches(msgKey, keyConfirm):
			server, op := next.confirm, next.confirmOp
			next.confirm, next.confirmOp = "", core.OpNone
			return &next, tui.Action(op, server)
		case key.Matches(msgKey, keyCancel):
			next.confirm, next.confirmOp = "", core.OpNone
		}
		return &next, nil
	}

	switch {
	case key.Matches(msgKey, keyUp):
		return &next, tui.Select(neighbour(snap, target, -1))
	case key.Matches(msgKey, keyDown):
		return &next, tui.Select(neighbour(snap, target, +1))
	case key.Matches(msgKey, keyStart):
		if target != "" {
			return &next, tui.Action(core.OpStart, target)
		}
	case key.Matches(msgKey, keyStop):
		// Uppercase, so it confirms. Stopping is not world-losing and does
		// not ask for the server's name typed out — that friction is
		// reserved for the three actions that can destroy a save.
		if target != "" {
			next.confirm, next.confirmOp = target, core.OpStop
		}
	case key.Matches(msgKey, keyRestart):
		if target != "" {
			next.confirm, next.confirmOp = target, core.OpRestart
		}
	case key.Matches(msgKey, keyBackup):
		// Lowercase and immediate: a backup interrupts nothing and the
		// worst outcome is a file you did not need.
		if target != "" {
			return &next, tui.Action(core.OpBackup, target)
		}
	case key.Matches(msgKey, keyUpdate):
		if target != "" {
			next.confirm, next.confirmOp = target, core.OpUpdate
		}
	}
	return &next, nil
}

// selectedOr falls back to the first row when nothing is selected, so the
// table always shows where a key press would land.
func selectedOr(selected string, snap core.Snapshot) string {
	if selected != "" {
		return selected
	}
	if len(snap.Servers) > 0 {
		return snap.Servers[0].Name
	}
	return ""
}

// neighbour is the server delta rows from the named one, clamped. Moving in
// the table moves the rail too, because they are one selection seen twice.
func neighbour(snap core.Snapshot, from string, delta int) string {
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
	target := selectedOr(f.Server, snap)
	v.snap = snap

	var b strings.Builder

	// The strip at the top answers "is anything wrong" from across the room.
	// Under 30 rows it collapses to a line of values, which is the narrow
	// layout rather than a clipped wide one.
	if f.Height >= 26 {
		b.WriteString(comp.TileStrip(t, f.Width, tiles(snap)))
	} else {
		b.WriteString(comp.InlineTiles(t, f.Width, tiles(snap)))
	}
	b.WriteString("\n")

	b.WriteString(comp.Panel{
		Theme:   t,
		Title:   "SERVERS",
		Right:   hints(v.confirm != ""),
		Width:   f.Width,
		Focused: f.Focused,
	}.Render(v.table(f, snap, target)))
	b.WriteString("\n")

	// Attention and activity sit side by side, and only when there is room
	// left after the table.
	remaining := f.Height - 8 - len(snap.Servers) - 4
	if remaining >= 6 {
		left := f.Width / 2
		right := f.Width - left - 1
		b.WriteString(comp.Columns(1,
			comp.Panel{Theme: t, Title: "ATTENTION", Right: "a ack", Width: left, Height: remaining}.
				Render(attention(f, snap, remaining-2)),
			comp.Panel{Theme: t, Title: "ACTIVITY", Right: "all servers", Width: right, Height: remaining}.
				Render(activity(f, snap, remaining-2)),
		))
		b.WriteString("\n")
	}

	return b.String()
}

// tiles is the fleet at a glance.
//
// The CPU and memory figures are the fleet's, not the machine's: Garrison sees
// what the engine reports for containers it manages, and inventing a
// host-wide number from that would be a guess presented as a measurement. The
// capacity beside each one comes from the engine, so the proportion is real.
func tiles(snap core.Snapshot) []comp.Tile {
	var players int
	var cpu, mem float64
	for _, srv := range snap.Servers {
		players += len(srv.Players)
		if p, ok := srv.CPU.Last(); ok {
			cpu += p.Mean
		}
		if p, ok := srv.Mem.Last(); ok {
			mem += p.Mean
		}
	}

	cores := snap.Engine.NCPU
	coreNote := ""
	if cores > 0 {
		coreNote = strconv.Itoa(cores) + "c"
	}
	memNote := ""
	if snap.Engine.MemTotal > 0 {
		memNote = comp.Bytes(snap.Engine.MemTotal)
	}

	attention := 0
	for _, n := range snap.Notices {
		if n.Level != core.LevelInfo {
			attention++
		}
	}

	return []comp.Tile{
		{
			Label:  "PLAYERS",
			Value:  strconv.Itoa(players),
			Points: fleetSeries(snap, func(s core.Server) model.History { return model.History{} }),
		},
		{
			Label:  "FLEET CPU",
			Right:  coreNote,
			Value:  fmt.Sprintf("%.0f%%", cpu),
			Points: sumSeries(snap, func(s core.Server) model.History { return s.CPU }),
			Min:    0,
			Max:    float64(cores) * 100,
		},
		{
			Label:  "FLEET MEM",
			Right:  memNote,
			Value:  comp.Bytes(int64(mem)),
			Points: sumSeries(snap, func(s core.Server) model.History { return s.Mem }),
			Min:    0,
			Max:    float64(snap.Engine.MemTotal),
		},
		{
			Label:  "ATTENTION",
			Right:  "a",
			Value:  strconv.Itoa(attention),
			Note:   attentionSummary(snap),
			Accent: attention > 0,
		},
	}
}

// sumSeries adds one series across the fleet, point for point from the newest
// backwards. Servers sample independently so the points do not line up exactly;
// aligning by position is close enough for a strip 21 cells wide, and the
// alternative is interpolating a picture nobody reads that precisely.
func sumSeries(snap core.Snapshot, pick func(core.Server) model.History) []model.Point {
	var longest int
	for _, srv := range snap.Servers {
		if n := len(pick(srv).Hot); n > longest {
			longest = n
		}
	}
	if longest == 0 {
		return nil
	}

	out := make([]model.Point, longest)
	for _, srv := range snap.Servers {
		hot := pick(srv).Hot
		offset := longest - len(hot)
		for i, p := range hot {
			out[offset+i].Mean += p.Mean
			out[offset+i].At = p.At
		}
	}
	return out
}

func fleetSeries(core.Snapshot, func(core.Server) model.History) []model.Point { return nil }

func attentionSummary(snap core.Snapshot) string {
	var crashed, ill int
	for _, srv := range snap.Servers {
		if srv.State == model.StateCrashed {
			crashed++
		}
		if unhealthy(srv) {
			ill++
		}
	}

	var parts []string
	if crashed > 0 {
		parts = append(parts, strconv.Itoa(crashed)+" crash")
	}
	if ill > 0 {
		parts = append(parts, strconv.Itoa(ill)+" unhealthy")
	}
	if len(parts) == 0 {
		return "all clear"
	}
	return strings.Join(parts, " · ")
}

// attention is the alert pane, with the host panel beneath it.
func attention(f tui.Frame, snap core.Snapshot, height int) string {
	t := f.Theme
	width := comp.Inner(f.Width / 2)

	var b strings.Builder
	shown := 0
	room := height - 5

	// A crashed or unhealthy server is not a Notice — nothing raised it, it
	// simply is — but it is certainly something that needs attention, and a
	// pane that omits it while the tile above counts it is a pane you stop
	// believing.
	for _, srv := range snap.Servers {
		if shown >= room {
			break
		}
		reason := srv.Detail
		switch {
		case srv.State == model.StateCrashed:
			if reason == "" {
				reason = "crashed"
			}
			b.WriteString(t.Err.Render(comp.Truncate(glyphFor(t, true)+" "+srv.Name+" — "+reason, width)))
		case unhealthy(srv):
			b.WriteString(t.Accent.Render(comp.Truncate(glyphFor(t, false)+" "+srv.Name+" — "+srv.Health.Detail, width)))
		default:
			continue
		}
		b.WriteString("\n")
		shown++
	}

	for i := len(snap.Notices) - 1; i >= 0 && shown < room; i-- {
		n := snap.Notices[i]
		if n.Level == core.LevelInfo {
			continue
		}

		style := t.Accent
		if n.Level == core.LevelError {
			style = t.Err
		}
		b.WriteString(style.Render(comp.Truncate(glyphFor(t, n.Level == core.LevelError)+" "+n.Text, width)))
		b.WriteString("\n")
		shown++
	}

	if shown == 0 {
		b.WriteString(t.Dim.Render("Nothing needs attention."))
		b.WriteString("\n")
	}

	// The host panel shares the pane, because "is the machine alright" is
	// the same question as "is anything wrong" asked one level down.
	for b.Len() > 0 && strings.Count(b.String(), "\n") < height-4 {
		b.WriteString("\n")
	}
	b.WriteString(t.Dim.Render(strings.Repeat("─", width)))
	b.WriteString("\n")
	b.WriteString(t.Header.Render("HOST"))
	b.WriteString("\n")
	b.WriteString(t.Dim.Render(comp.Truncate(hostLine(snap), width)))
	return b.String()
}

func glyphFor(t *comp.Theme, bad bool) string {
	if t.ASCII {
		if bad {
			return "x"
		}
		return "!"
	}
	if bad {
		return "✕"
	}
	return "!"
}

func hostLine(snap core.Snapshot) string {
	e := snap.Engine

	parts := []string{}
	if e.Version != "" {
		parts = append(parts, "docker "+e.Version)
	}
	if e.Transport != "" {
		parts = append(parts, e.Transport)
	}
	if e.OK {
		parts = append(parts, "healthy")
	} else {
		parts = append(parts, "unreachable")
	}
	return strings.Join(parts, " · ")
}

// activity is the fleet-wide feed: every server's classified output, merged
// and newest first.
func activity(f tui.Frame, snap core.Snapshot, height int) string {
	t := f.Theme
	width := comp.Inner(f.Width / 2)

	type line struct {
		at     time.Time
		server string
		kind   model.Kind
		text   string
	}

	// The feed shows the newest few events across the whole fleet, so it
	// needs a window from each server rather than the whole ring. Sized
	// well above the visible height because the Info lines filtered out
	// below can fill a window on a server that is starting up.
	const window = 512

	var all []line
	for _, srv := range snap.Servers {
		for _, ev := range srv.Console.Tail(window) {
			text := ev.Text
			if text == "" {
				text = ev.Raw
			}
			if strings.TrimSpace(text) == "" || ev.Kind == model.KindInfo {
				// The feed is for things that happened, not for a server
				// narrating its own startup.
				continue
			}
			all = append(all, line{at: ev.At, server: srv.Name, kind: ev.Kind, text: text})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.After(all[j].at) })

	if len(all) == 0 {
		return t.Dim.Render("Nothing yet.")
	}
	if len(all) > height {
		all = all[:height]
	}

	var b strings.Builder
	for _, l := range all {
		// Local time, and the day when it is not today. The instant is
		// stored in UTC because that is what the container's clock writes;
		// converting is comp.Stamp's job and no view does it itself.
		stamp := comp.Stamp(l.at, f.Now)
		server := l.server
		bodyWidth := width - comp.StampWidth - comp.Width(server) - 4
		body := comp.Pad(comp.Truncate(l.text, bodyWidth), bodyWidth)

		b.WriteString(t.Dim.Render(stamp) + " ")
		b.WriteString(comp.KindStyle(t, l.kind).Render(comp.KindGlyph(t, l.kind)) + " ")
		b.WriteString(body + " ")
		b.WriteString(t.Dim.Render(server))
		b.WriteString("\n")
	}
	return b.String()
}

func hints(confirming bool) string {
	if confirming {
		return "y confirm · n cancel"
	}
	return "u start · S stop · r restart · b backup · U update"
}

// confirmPrompt says what is about to happen, naming the server. "Confirm?" on
// its own is a question nobody can answer safely.
func confirmPrompt(op core.Op, server string) string {
	switch op {
	case core.OpRestart:
		return "restart " + server + "?  it will be down for a moment  ·  y / n"
	case core.OpUpdate:
		return "update " + server + "?  the world is snapshotted first  ·  y / n"
	}
	return "stop " + server + "?  y / n"
}

// columns is the width budget for one table row, recomputed per frame.
//
// Every view has a real narrow layout rather than a clipped wide one, so the
// columns that stop earning their space are dropped rather than squeezed:
// ports go first, then game, then the note.
type columns struct {
	name, game, state, plyr, cpu, mem, uptime, last int
}

func layout(width int) columns {
	c := columns{name: 16, game: 9, state: 11, plyr: 4, cpu: 6, mem: 9, uptime: 7}

	fixed := func(c columns) int {
		n := 2 + c.name + c.state + c.plyr + c.cpu + c.mem + c.uptime + 6
		if c.game > 0 {
			n += c.game + 1
		}
		return n
	}

	// Game goes before the last column narrows past readable: the state
	// glyph already distinguishes the rows, and "16261/udp" or "exit 137"
	// is the thing you came to read.
	if width < fixed(c)+14 {
		c.game = 0
	}
	if width < fixed(c)+14 {
		c.name = 13
	}

	c.last = width - fixed(c)
	if c.last < 0 {
		c.last = 0
	}
	return c
}

func (v *View) table(f tui.Frame, snap core.Snapshot, target string) string {
	t := f.Theme
	c := layout(comp.Inner(f.Width))

	if len(snap.Servers) == 0 {
		return emptyExplanation(t, snap)
	}

	shown := v.matching(snap)
	if len(shown) == 0 {
		return t.Dim.Render("Nothing matching \"" + v.filter.Query + "\". esc clears the filter.")
	}

	var b strings.Builder
	b.WriteString(t.Header.Render(header(c)))
	b.WriteString("\n")
	for _, srv := range shown {
		b.WriteString(v.row(f, c, srv, srv.Name == target))
		b.WriteString("\n")
	}

	if bar := v.filter.Render(t, comp.Inner(f.Width)); bar != "" {
		b.WriteString("\n" + bar)
	}

	if v.confirm != "" {
		b.WriteString("\n")
		b.WriteString(t.Accent.Render(confirmPrompt(v.confirmOp, v.confirm)))
	}
	return strings.TrimRight(b.String(), "\n")
}

// matching is the servers the filter lets through, in fleet order.
//
// Name, game and state, because those are what a person squints at the table
// for: "the zomboid one", "the crashed one".
func (v *View) matching(snap core.Snapshot) []core.Server {
	if !v.filter.On() {
		return snap.Servers
	}
	out := make([]core.Server, 0, len(snap.Servers))
	for _, srv := range snap.Servers {
		if v.filter.Matches(srv.Name, srv.Game, srv.State.String()) {
			out = append(out, srv)
		}
	}
	return out
}

func header(c columns) string {
	cells := []string{comp.Pad("  NAME", c.name+2)}
	if c.game > 0 {
		cells = append(cells, comp.Pad("GAME", c.game))
	}
	cells = append(cells,
		comp.Pad("STATE", c.state),
		comp.PadLeft("PLYR", c.plyr),
		comp.PadLeft("CPU", c.cpu),
		comp.PadLeft("MEM", c.mem),
		comp.PadLeft("UPTIME", c.uptime),
		comp.Pad("PORTS / TASK", c.last),
	)
	return strings.TrimRight(strings.Join(cells, " "), " ")
}

func (v *View) row(f tui.Frame, c columns, srv core.Server, selected bool) string {
	t := f.Theme

	cursor := "  "
	if selected {
		marker := "▌"
		if t.ASCII {
			marker = ">"
		}
		cursor = t.Accent.Render(marker) + " "
	}

	nameStyle := t.Dim
	if selected {
		nameStyle = t.Selected
	}

	cells := []string{cursor + nameStyle.Render(comp.Pad(srv.Name, c.name))}
	if c.game > 0 {
		cells = append(cells, t.Dim.Render(comp.Pad(srv.Game, c.game)))
	}
	cells = append(cells,
		stateCell(t, srv.State, c.state),
		t.Dim.Render(comp.PadLeft(playerCount(srv), c.plyr)),
		t.Dim.Render(comp.PadLeft(cpuCell(srv), c.cpu)),
		t.Dim.Render(comp.PadLeft(memCell(srv), c.mem)),
		t.Dim.Render(comp.PadLeft(comp.Duration(srv.Uptime(f.Now)), c.uptime)),
		v.lastCell(f, t, srv, c.last),
	)
	return strings.TrimRight(strings.Join(cells, " "), " ")
}

// stateCell is the glyph and the word in one column, so the two can never
// drift apart and the header only has to describe one field.
func stateCell(t *comp.Theme, state model.State, width int) string {
	return t.StateStyle(state).Render(t.StateGlyph(state) + " " + comp.Pad(t.StateWord(state), width-2))
}

func playerCount(srv core.Server) string {
	if !srv.State.Live() {
		return "—"
	}
	return strconv.Itoa(len(srv.Players))
}

func cpuCell(srv core.Server) string {
	p, ok := srv.CPU.Last()
	if !ok || !srv.State.Live() {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", p.Mean)
}

func memCell(srv core.Server) string {
	p, ok := srv.Mem.Last()
	if !ok || !srv.State.Live() {
		return "—"
	}
	return comp.Bytes(int64(p.Mean))
}

// lastCell is one column carrying whichever of three things the row most needs
// to say, in that order: what is happening to it, why it is not running, and
// where to reach it.
//
// One column rather than three because they are never all interesting at once
// — a server being restarted is not also telling you its ports — and the width
// it saves is what lets the game column survive beside the rail.
func (v *View) lastCell(f tui.Frame, t *comp.Theme, srv core.Server, width int) string {
	// A task in flight beats everything else: the operator pressed the key
	// that started it and wants to see where it got to.
	if task, ok := v.snap.TaskFor(srv.Name); ok {
		return taskCell(t, task, width)
	}
	if srv.Busy != core.OpNone {
		return t.Accent.Render(comp.Pad(busyText(srv), width))
	}
	if srv.Detail != "" {
		style := t.Dim
		switch {
		case srv.State == model.StateCrashed, srv.State == model.StateUnknown:
			style = t.Err
		case srv.State == model.StateStopped && srv.ExitCode != 0:
			// Deliberately down, but it did not go quietly. The glyph stays
			// grey because the state is honest — Garrison asked for this —
			// while the reason stays loud, because a server killed before
			// it finished writing is a save you may not have.
			style = t.Err
		}
		return style.Render(comp.Pad(srv.Detail, width))
	}
	if unhealthy(srv) {
		return t.Err.Render(comp.Pad("unhealthy: "+srv.Health.Detail, width))
	}
	if srv.State.Live() {
		return t.Dim.Render(comp.Pad(portList(srv.Ports), width))
	}
	return comp.Pad("", width)
}

// unhealthy reports a failed healthcheck.
//
// A zero model.Health means nobody has looked, not that the answer was bad —
// the driver reports OK for a container with no healthcheck declared, because
// absence of a check is not evidence of ill health. Treating the zero value as
// a failure paints every server amber the moment anything constructs a
// Container without filling it in.
func unhealthy(srv core.Server) bool {
	return !srv.Health.OK && srv.Health.Detail != ""
}

// busyText says what is happening and, for a stop, how long it may take.
//
// A game server is asked to leave and then given time to finish writing, so a
// stop is a wait rather than an instant — sixty seconds of an unqualified
// "stopping…" is how an operator concludes it has hung and reaches for
// something less patient than the grace period.
func busyText(srv core.Server) string {
	if srv.Busy == core.OpStop && srv.StopGrace > 0 {
		return "stopping… up to " + core.Budget(srv.StopGrace)
	}
	return srv.Busy.Present() + "…"
}

// taskCell shows how far a running task has got, which is what the mockup's
// PORTS / TASK column carries for a server something is happening to.
func taskCell(t *comp.Theme, task engine.Progress, width int) string {
	label := fmt.Sprintf("%s %d/%d %s", task.Kind, task.Cursor+1, len(task.Steps), task.StepName())
	return t.Accent.Render(comp.Pad(comp.Truncate(label, width), width))
}

func portList(ports []model.PortMap) string {
	if len(ports) == 0 {
		return "—"
	}
	out := make([]string, 0, len(ports))
	for _, p := range ports {
		out = append(out, strconv.Itoa(p.Host)+"/"+proto(p.Container))
	}
	return strings.Join(out, " ")
}

func proto(spec string) string {
	if _, p, ok := strings.Cut(spec, "/"); ok {
		return p
	}
	return "tcp"
}

// emptyExplanation is the difference between a view that looks broken and one
// that tells you what to do. An empty fleet has two causes and they need
// different answers.
func emptyExplanation(t *comp.Theme, snap core.Snapshot) string {
	if !snap.Engine.OK {
		return t.Err.Render("The container engine is not answering.") + "\n" +
			t.Dim.Render("Garrison keeps trying; the fleet will fill in on its own.")
	}
	return t.Dim.Render("No containers labelled garrison.managed=1.") + "\n" +
		t.Dim.Render("The fleet is found by label, so anything Garrison created appears here.")
}

// Capturing is true while a confirmation is up or the filter has the
// keyboard. Both take keys the shell also binds — "y" answers a stop, and a
// server name being typed into the filter contains letters that are verbs.
func (v *View) Capturing() bool { return v.confirm != "" || v.filter.Active }
