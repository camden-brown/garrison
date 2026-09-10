// Package console is the Console view: a server's classified output, with the
// command line that talks back to it.
//
// There is no game-specific code here. Lines arrive already classified by the
// plugin's Parse, so the colours and glyphs derive from model.Kind rather than
// from knowing what Valheim's log looks like; and whether a command can be
// sent at all is a capability assertion, so a game that grows RCON lights the
// input up without this file changing.
package console

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/games"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tui"
	"github.com/camden-brown/garrison/internal/tui/comp"
)

// filter narrows the log to one class of line.
//
// It is a fixed set rather than a text search because the classification is
// the thing the plugin already did: asking for "chat" is asking for a Kind,
// and matching a substring would find the word "chat" in an error message.
type filter uint8

const (
	filterAll filter = iota
	filterChat
	filterPlayers
	filterProblems
)

var filterNames = [...]string{"all", "chat", "players", "problems"}

func (f filter) String() string { return filterNames[int(f)%len(filterNames)] }

// keeps reports whether a line survives the filter.
func (f filter) keeps(k model.Kind) bool {
	switch f {
	case filterChat:
		return k == model.KindChat
	case filterPlayers:
		return k == model.KindJoin || k == model.KindLeave || k == model.KindDeath
	case filterProblems:
		return k == model.KindError || k == model.KindWarn
	}
	return true
}

// View is the console.
//
// Everything it holds is cursor, scroll or filter, which is all a view is
// allowed to own. The lines themselves live in the store's ring, so scrolling
// back four hours costs nothing but an index.
type View struct {
	// scroll is how many lines above the newest the window sits. Zero
	// follows the tail, which is where a console should be unless somebody
	// deliberately went looking.
	scroll int

	// frozen stops the view following new output. It does not stop the
	// output: the ring keeps filling, and unfreezing catches up.
	//
	// frozenAt is how many lines there were when the freeze began. Without
	// it "frozen" would mean nothing — the window is measured from the end,
	// so it would slide along with every new line and freeze would be a
	// label rather than a behaviour.
	frozen   bool
	frozenAt int

	filter filter

	// text is the "/" filter, which narrows by content where filter narrows
	// by classification. Both are the view's to own.
	text comp.Filter
}

func New() *View { return &View{} }

func (v *View) ID() tui.ViewID { return tui.ViewConsole }
func (v *View) Title() string  { return "Console" }

// Available refuses without a server, because a console belongs to one.
func (v *View) Available(inst model.Instance) (bool, string) {
	if inst.Name == "" {
		return false, "Pick a server first — a console belongs to one."
	}
	return true, ""
}

var (
	keyUp     = key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "back"))
	keyDown   = key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "forward"))
	keyPageUp = key.NewBinding(key.WithKeys("pgup", "ctrl+b"), key.WithHelp("pgup", "page back"))
	keyPageDn = key.NewBinding(key.WithKeys("pgdown", "ctrl+f"), key.WithHelp("pgdn", "page forward"))
	keyTop    = key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "oldest"))
	keyBottom = key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "newest"))
	keyFreeze = key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "freeze"))
	keyFilter = key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "class filter"))
	keySearch = key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter text"))
)

func (v *View) Keys() []key.Binding {
	return []key.Binding{keyUp, keyDown, keyPageUp, keyPageDn, keyTop, keyBottom, keyFreeze, keyFilter, keySearch}
}

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

	// The filter bar swallows every key while it is open, or typing "f"
	// into a search cycles the class filter underneath it.
	if text, handled := next.text.Key(msgKey); handled {
		next.text = text
		next.scroll = 0
		return &next, nil
	}
	if msgKey.String() == "/" {
		next.text = next.text.Open()
		return &next, nil
	}

	lines := next.visible(srv)
	page := rows(f)
	total := next.total(len(lines))

	switch {
	case key.Matches(msgKey, keyUp):
		next.scrollBy(+1, total, page)
	case key.Matches(msgKey, keyDown):
		next.scrollBy(-1, total, page)
	case key.Matches(msgKey, keyPageUp):
		next.scrollBy(+page, total, page)
	case key.Matches(msgKey, keyPageDn):
		next.scrollBy(-page, total, page)
	case key.Matches(msgKey, keyTop):
		next.scroll = max(0, next.total(len(lines))-page)
	case key.Matches(msgKey, keyBottom):
		next.scroll = 0
	case key.Matches(msgKey, keyFreeze):
		next.frozen = !next.frozen
		next.frozenAt = 0
		if next.frozen {
			next.frozenAt = len(lines)
		}
	case key.Matches(msgKey, keyFilter):
		next.filter = (next.filter + 1) % filter(len(filterNames))
		// A filter change with fewer lines behind it would leave the window
		// past the end, showing blank rows above real ones.
		next.scroll = 0
	}
	return &next, nil
}

// scrollBy moves the window, clamped so it cannot run off either end.
//
// Scrolling to the newest line rather than past it is what makes the freeze
// key honest: the view is either following or parked somewhere real.
func (v *View) scrollBy(delta, total, page int) {
	v.scroll += delta
	if top := total - page; v.scroll > top {
		v.scroll = top
	}
	if v.scroll < 0 {
		v.scroll = 0
	}
}

// total is how many lines the window may reach: everything when following,
// and no further than where the freeze began when frozen.
func (v *View) total(lines int) int {
	if v.frozen && v.frozenAt > 0 && v.frozenAt < lines {
		return v.frozenAt
	}
	return lines
}

// visible is the collapsed, filtered lines for this server, oldest first.
func (v *View) visible(srv core.Server) []comp.LogLine {
	all := comp.CollapseRepeats(srv.Console.Events())
	if v.filter == filterAll && !v.text.On() {
		return all
	}
	out := make([]comp.LogLine, 0, len(all))
	for _, l := range all {
		if v.filter.keeps(l.Event.Kind) && v.text.Matches(l.Text(), l.Event.Player) {
			out = append(out, l)
		}
	}
	return out
}

// rows is how many log lines fit: the frame less the panel's own chrome and
// the command line beneath it.
func rows(f tui.Frame) int {
	n := f.Height - 5
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

	lines := v.visible(srv)
	page := rows(f)

	end := v.total(len(lines)) - v.scroll
	if end > len(lines) {
		end = len(lines)
	}
	if end < 0 {
		end = 0
	}
	start := end - page
	if start < 0 {
		start = 0
	}

	width := comp.Inner(f.Width)

	var b strings.Builder
	if len(lines) == 0 {
		b.WriteString(t.Dim.Render(v.emptyText(srv)))
	}
	// A day separator rather than a date on every row: a console is read
	// downwards and mostly covers one day, so repeating it on each line is
	// noise — but crossing midnight without saying so is how a crash gets
	// blamed on the wrong evening.
	var previous time.Time
	for i, line := range lines[start:end] {
		at := line.Event.At
		if !at.IsZero() && (i == 0 || !comp.SameDay(at, previous)) {
			if day := comp.StampDay(at, f.Now); day != "" {
				b.WriteString(t.Header.Render(comp.Truncate("── "+day+" ", width)))
				b.WriteString("\n")
			}
		}
		if !at.IsZero() {
			previous = at
		}
		b.WriteString(renderLine(t, line, width))
		b.WriteString("\n")
	}

	body := comp.Panel{
		Theme:   t,
		Title:   "CONSOLE",
		Right:   v.status(srv),
		Width:   f.Width,
		Focused: f.Focused,
	}.Render(strings.TrimRight(b.String(), "\n"))

	if bar := v.text.Render(t, f.Width); bar != "" {
		return body + "\n" + bar + "\n" + v.commandLine(f, srv)
	}
	return body + "\n" + v.commandLine(f, srv)
}

// renderLine is one row: when, what class, what it said, and how many times.
func renderLine(t *comp.Theme, line comp.LogLine, width int) string {
	// The day comes from the separator above, so the row carries only the
	// time — see the note on comp.StampTime.
	stamp := comp.StampTime(line.Event.At)

	text := line.Text()
	if line.Count > 1 {
		// The counter is the point of collapsing, so it is styled as
		// content rather than dimmed away with the timestamp.
		text += fmt.Sprintf("  %d×", line.Count)
	}

	prefix := t.Dim.Render(stamp) + " " +
		comp.KindStyle(t, line.Event.Kind).Render(comp.KindGlyph(t, line.Event.Kind)) + " "
	return prefix + comp.KindStyle(t, line.Event.Kind).Render(
		comp.Truncate(text, width-comp.TimeWidth-3))
}

// status is the right-hand side of the panel title: where the window is and
// what it is hiding.
func (v *View) status(srv core.Server) string {
	parts := []string{srv.Name}
	if v.filter != filterAll {
		parts = append(parts, "filter: "+v.filter.String())
	}
	if v.frozen {
		parts = append(parts, "FROZEN")
	} else if v.scroll > 0 {
		parts = append(parts, fmt.Sprintf("%d back", v.scroll))
	}
	if dropped := srv.Console.Dropped(); dropped > 0 {
		parts = append(parts, fmt.Sprintf("%d scrolled off", dropped))
	}
	return strings.Join(parts, " · ")
}

func (v *View) emptyText(srv core.Server) string {
	if v.text.On() {
		return "Nothing matching \"" + v.text.Query + "\". esc clears it."
	}
	if v.filter != filterAll {
		return "Nothing matching " + v.filter.String() + ". f cycles the filter."
	}
	if srv.State != model.StateRunning {
		return "Nothing yet — the server is not running."
	}
	return "Nothing yet. Output appears as the server produces it."
}

// commandLine is the input beneath the log, or the reason there is not one.
//
// Whether a command can be sent is games.Commandable and nothing else. A game
// that has no interactive channel gets a sentence saying so, which is more use
// than an input that accepts a command and drops it — and the assertion is
// what keeps this file from ever learning which games those are.
func (v *View) commandLine(f tui.Frame, srv core.Server) string {
	t := f.Theme

	g, err := games.Get(srv.Game)
	if err != nil {
		return t.Dim.Render("> no plugin for " + srv.Game + ", so no command channel.")
	}
	if _, ok := g.(games.Commandable); !ok {
		return t.Dim.Render("> " + g.Meta().Name + " has no command channel — nothing to send commands over.")
	}
	return t.Dim.Render("> command input arrives with the RCON transport.")
}
