// Package mods is the Mods view: what a server loads, in what order.
//
// There is no game-specific code here and no list of which games have mods.
// Whether a server's mods can be managed at all is games.Moddable, and whether
// their order is meaningful is that interface's own answer — so a game that
// grows a mod system lights this screen up without the file changing, and one
// that has none gets a sentence rather than an empty table.
package mods

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/games"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tui"
	"github.com/camden-brown/garrison/internal/tui/comp"
)

// View lists a server's configured mods.
type View struct {
	cursor int
}

func New() *View { return &View{} }

func (v *View) ID() tui.ViewID { return tui.ViewMods }
func (v *View) Title() string  { return "Mods" }

// Available refuses when the game has no mod system, and says so in its own
// terms rather than the shell's.
//
// This is the capability degradation DESIGN asks for: the answer to "why is
// this screen empty" is on the screen, and it comes from asserting an
// interface rather than from knowing which games those are.
func (v *View) Available(inst model.Instance) (bool, string) {
	if inst.Name == "" {
		return false, "Pick a server first — mods belong to one."
	}
	g, err := games.Get(inst.Game)
	if err != nil {
		return false, fmt.Sprintf("No plugin for %q, so Garrison does not know its mods.", inst.Game)
	}
	if _, ok := g.(games.Moddable); !ok {
		return false, g.Meta().Name + " has no mod system Garrison can manage."
	}
	return true, ""
}

var (
	keyUp    = key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up"))
	keyDown  = key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down"))
	keyRaise = key.NewBinding(key.WithKeys("K", "shift+up"), key.WithHelp("K", "earlier"))
	keyLower = key.NewBinding(key.WithKeys("J", "shift+down"), key.WithHelp("J", "later"))
	// The same key as the settings form, because it is the same act: the
	// configuration on disk is ahead of the server, and applying catches the
	// server up. A mod list is settings that happen to be files.
	keyApply = key.NewBinding(key.WithKeys("A"), key.WithHelp("A", "apply"))
)

func (v *View) Keys() []key.Binding {
	return []key.Binding{keyUp, keyDown, keyRaise, keyLower, keyApply}
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
	n := len(srv.Instance.Mods)

	switch {
	case key.Matches(msgKey, keyUp):
		next.cursor--
	case key.Matches(msgKey, keyDown):
		next.cursor++

	case key.Matches(msgKey, keyRaise), key.Matches(msgKey, keyLower):
		// Reordering only where it means something. Offering it for a game
		// that loads mods in whatever order it likes would be a key that
		// rewrites config and changes nothing.
		if !ordered(srv) || n < 2 {
			return &next, nil
		}
		to := next.cursor - 1
		if key.Matches(msgKey, keyLower) {
			to = next.cursor + 1
		}
		if to < 0 || to >= n {
			return &next, nil
		}
		from := next.cursor
		// The cursor follows the mod rather than staying put, so holding K
		// walks one mod up the list instead of walking the list past the
		// cursor.
		next.cursor = to
		return &next, tui.ReorderMods(srv.Name, from, to)

	case key.Matches(msgKey, keyApply):
		// ApplyNow rather than Apply: a mod list is edited in the server's
		// TOML, so there is never a draft pending and an apply that waits
		// for one is a key that does nothing.
		//
		// Always a recreate. For a game that fetches its own mods the ids
		// have just been written into config the server reads at boot, and
		// for one whose mods are files the loader only looks at them when it
		// starts — either way nothing a running container has already read
		// changes.
		return &next, tui.ApplyNow(srv.Name)
	}

	next.clamp(n)
	return &next, nil
}

// ordered reports whether this server's game cares about load order.
func ordered(srv core.Server) bool {
	g, err := games.Get(srv.Game)
	if err != nil {
		return false
	}
	m, ok := g.(games.Moddable)
	return ok && m.LoadOrderMatters()
}

func (v *View) clamp(n int) {
	if v.cursor >= n {
		v.cursor = n - 1
	}
	if v.cursor < 0 {
		v.cursor = 0
	}
}

func (v *View) Render(f tui.Frame, snap core.Snapshot) string {
	t := f.Theme

	srv, ok := snap.Server(f.Server)
	if !ok {
		return t.Dim.Render("No such server.")
	}
	v.clamp(len(srv.Instance.Mods))

	g, err := games.Get(srv.Game)
	if err != nil {
		return t.Dim.Render("No plugin for " + srv.Game + ".")
	}
	m, ok := g.(games.Moddable)
	if !ok {
		return t.Dim.Render(g.Meta().Name + " has no mod system Garrison can manage.")
	}

	body := comp.Panel{
		Theme:   t,
		Title:   "MODS",
		Right:   right(srv, m),
		Width:   f.Width,
		Focused: f.Focused,
	}.Render(v.list(f, srv, m))

	return body + "\n" + t.Dim.Render(comp.Truncate(footer(m), f.Width))
}

func (v *View) list(f tui.Frame, srv core.Server, m games.Moddable) string {
	t := f.Theme
	width := comp.Inner(f.Width)

	if len(srv.Instance.Mods) == 0 {
		if !srv.Configured {
			// A container found by label with no file behind it. Garrison
			// has nowhere to read a mod list from, which is not the same as
			// there being none.
			return t.Dim.Render("Garrison has no configuration for this server, so it cannot know its mods.")
		}
		return t.Dim.Render("No mods configured. They are the [[mods]] entries in the server's TOML.")
	}

	ordered := m.LoadOrderMatters()

	var b strings.Builder
	for i, ref := range srv.Instance.Mods {
		selected := i == v.cursor

		marker := "  "
		if selected {
			marker = "▌ "
			if t.ASCII {
				marker = "> "
			}
		}

		// The position is shown only when it means something. A number
		// beside a list whose order is irrelevant is a number somebody will
		// try to change.
		const positionWidth = 5
		position := strings.Repeat(" ", positionWidth)
		if ordered {
			position = comp.Pad(comp.PadLeft(fmt.Sprintf("%d.", i+1), 3), positionWidth)
		}

		version := ref.Pin
		if version == "" {
			version = "latest"
		}

		// The resolved name where a resolver has found one, the id until
		// then. The id is what the operator wrote and never stops being
		// true, so it is the fallback rather than a blank.
		label := ref.ID
		note := source(m)
		resolved, found := resolvedFor(srv, ref.ID)
		if found {
			if resolved.Name != "" {
				label = resolved.Name
			}
			switch {
			case resolved.Err != "":
				note = resolved.Err
			case resolved.NeedsUpdate():
				note = "update available (" + resolved.Available + ")"
			case resolved.SizeBytes > 0:
				note = source(m) + " · " + comp.Bytes(resolved.SizeBytes)
			}
		}

		noteStyle := t.Dim
		if found && (resolved.Err != "" || resolved.NeedsUpdate()) {
			// A mod that cannot be found and one that has moved on are both
			// things to act on, so neither is dim.
			noteStyle = t.Accent
			if resolved.Err != "" {
				noteStyle = t.Err
			}
		}

		b.WriteString(t.On(t.Accent, selected).Render(marker))
		b.WriteString(t.On(t.Dim, selected).Render(position))
		b.WriteString(t.On(t.Title, selected).Render(comp.Pad(label, 30)))
		b.WriteString(t.On(t.Dim, selected).Render(comp.Pad(version, 16)))
		b.WriteString(t.On(noteStyle, selected).Render(
			comp.Truncate(note, width-2-comp.Width(position)-30-16)))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// resolvedFor is what a resolver found out about one configured mod.
func resolvedFor(srv core.Server, id string) (model.Mod, bool) {
	for _, m := range srv.Mods {
		if m.ID == id {
			return m, true
		}
	}
	return model.Mod{}, false
}

func right(srv core.Server, m games.Moddable) string {
	n := len(srv.Instance.Mods)
	if n == 0 {
		return srv.Name
	}
	if m.LoadOrderMatters() {
		return fmt.Sprintf("%s · %d mods · load order matters", srv.Name, n)
	}
	return fmt.Sprintf("%s · %d mods", srv.Name, n)
}

func source(m games.Moddable) string {
	switch m.ModSource() {
	case games.ModSourceWorkshop:
		return "Steam Workshop"
	case games.ModSourceThunderstore:
		return "Thunderstore"
	case games.ModSourceURL:
		return "URL"
	}
	return ""
}

// footer says what this screen cannot do yet, because a mod list you cannot
// act on should say so rather than leave you hunting for the key.
func footer(m games.Moddable) string {
	if m.LoadOrderMatters() {
		return "K / J moves a mod earlier or later — the order is the load order. A applies, which recreates the container."
	}
	return "Mods come from the server's TOML. A installs them and recreates the container."
}

// Capturing is false: this view has no modal and no text field, so the
// shell's keys keep their meanings here.
func (v *View) Capturing() bool { return false }
