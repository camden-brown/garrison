// Package backups is the Backups view: what has been archived, and the
// restore that puts one back.
//
// Restoring over a live world is one of the three actions DESIGN says must ask
// for the server's name typed out in full. That prompt is here rather than in
// the store, because it is a question about intent and the store only takes
// decisions. What the store guarantees instead is that the task archives the
// world it is about to replace and unwinds to it on any failure — so the
// prompt protects you from meaning the wrong thing, and the task protects you
// from everything else.
package backups

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

// View lists a server's archives.
type View struct {
	cursor int

	// confirming is the restore waiting for the server's name, and typed is
	// what has been entered so far with cursor at caret.
	//
	// This is the one place a view holds text rather than putting it in the
	// store, and the reason is the direction it fails in: a confirmation
	// lost to a resize is a destructive action that did not happen. Every
	// other draft in Garrison is worth preserving; this one is worth
	// dropping.
	confirming bool
	typed      string
	caret      int
}

// How much room the confirmation field wants, and the most it will take.
// Below minField the question is shortened rather than the field.
const (
	minField = 20
	maxField = 24
)

func New() *View { return &View{} }

func (v *View) ID() tui.ViewID { return tui.ViewBackups }
func (v *View) Title() string  { return "Backups" }

// Available refuses without a server, because archives belong to one.
func (v *View) Available(inst model.Instance) (bool, string) {
	if inst.Name == "" {
		return false, "Pick a server first — backups belong to one."
	}
	if inst.Data == "" {
		return false, "This server has no data directory configured, so there is nothing to archive."
	}
	return true, ""
}

var (
	keyUp      = key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up"))
	keyDown    = key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down"))
	keyBackup  = key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "back up now"))
	keyRestore = key.NewBinding(key.WithKeys("B"), key.WithHelp("B", "restore"))
	keyCancel  = key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"))
	keySubmit  = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm"))
)

func (v *View) Keys() []key.Binding {
	return []key.Binding{keyUp, keyDown, keyBackup, keyRestore}
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

	// The confirmation swallows every other key: the server's name is text,
	// and "b" is a letter in most of them.
	if next.confirming {
		switch {
		case key.Matches(msgKey, keyCancel):
			next.clear()
			return &next, nil

		case key.Matches(msgKey, keySubmit):
			if next.typed != srv.Name {
				// Wrong name is not an error to report — it is the prompt
				// doing its job. Clearing it makes the next attempt start
				// from nothing rather than from a near miss.
				next.typed, next.caret = "", 0
				return &next, nil
			}
			archive, ok := next.selected(srv)
			next.clear()
			if !ok {
				return &next, nil
			}
			return &next, tui.Restore(srv.Name, archive.Path)
		}

		typed, caret, handled := comp.EditKey(next.typed, next.caret, msgKey)
		if handled {
			next.typed, next.caret = typed, caret
		}
		return &next, nil
	}

	switch {
	case key.Matches(msgKey, keyUp):
		next.move(-1, len(srv.Backups))
	case key.Matches(msgKey, keyDown):
		next.move(+1, len(srv.Backups))
	case key.Matches(msgKey, keyBackup):
		return &next, tui.Action(core.OpBackup, srv.Name)
	case key.Matches(msgKey, keyRestore):
		if _, ok := next.selected(srv); ok {
			next.confirming = true
			next.typed, next.caret = "", 0
		}
	}
	return &next, nil
}

func (v *View) clear() {
	v.confirming = false
	v.typed = ""
	v.caret = 0
}

func (v *View) move(delta, n int) {
	v.cursor += delta
	if v.cursor >= n {
		v.cursor = n - 1
	}
	if v.cursor < 0 {
		v.cursor = 0
	}
}

func (v *View) selected(srv core.Server) (model.Archive, bool) {
	if len(srv.Backups) == 0 {
		return model.Archive{}, false
	}
	return srv.Backups[min(v.cursor, len(srv.Backups)-1)], true
}

func (v *View) Render(f tui.Frame, snap core.Snapshot) string {
	t := f.Theme

	srv, ok := snap.Server(f.Server)
	if !ok {
		return t.Dim.Render("No such server.")
	}
	v.move(0, len(srv.Backups))

	body := comp.Panel{
		Theme:   t,
		Title:   "BACKUPS",
		Right:   v.summary(srv),
		Width:   f.Width,
		Focused: f.Focused,
	}.Render(v.list(f, srv))

	return body + "\n" + v.footer(f, srv)
}

func (v *View) list(f tui.Frame, srv core.Server) string {
	t := f.Theme
	width := comp.Inner(f.Width)

	if len(srv.Backups) == 0 {
		if !srv.BackupsKnown {
			// Not the same as none, and saying "no backups" about a
			// directory nobody has read yet is how a screen loses trust.
			return t.Dim.Render("Looking…")
		}
		return t.Dim.Render("No backups yet. b takes one now.")
	}

	var b strings.Builder
	for i, a := range srv.Backups {
		selected := i == v.cursor

		marker := "  "
		if selected {
			marker = "▌ "
			if t.ASCII {
				marker = "> "
			}
		}

		// Archive names are UTC — backup.Name writes them that way so the
		// directory sorts chronologically — so this has to convert, or a
		// backup taken at seven in the morning reads as noon.
		when := a.Taken.Local().Format("2006-01-02 15:04")
		size := comp.PadLeft(comp.Bytes(a.Bytes), 10)
		age := comp.PadLeft(comp.Duration(f.Now.Sub(a.Taken))+" ago", 12)

		b.WriteString(t.On(t.Accent, selected).Render(marker))
		b.WriteString(t.On(t.Title, selected).Render(comp.Pad(when, 18)))
		b.WriteString(t.On(t.Dim, selected).Render(size))
		b.WriteString(t.On(t.Dim, selected).Render(age))
		b.WriteString(t.On(t.Dim, selected).Render(comp.Pad("  "+a.Name, width-2-18-10-12)))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// summary is the panel's right-hand side: how many and how much disk.
func (v *View) summary(srv core.Server) string {
	if len(srv.Backups) == 0 {
		return srv.Name
	}
	var total int64
	for _, a := range srv.Backups {
		total += a.Bytes
	}
	return fmt.Sprintf("%s · %d archives · %s", srv.Name, len(srv.Backups), comp.Bytes(total))
}

// footer is the prompt when confirming and the hint otherwise.
func (v *View) footer(f tui.Frame, srv core.Server) string {
	t := f.Theme

	if !v.confirming {
		if len(srv.Backups) == 0 {
			return t.Dim.Render("b back up now")
		}
		return t.Dim.Render("b back up now · B restore the selected archive")
	}

	archive, ok := v.selected(srv)
	if !ok {
		return t.Dim.Render("Nothing selected.")
	}

	// The name is asked for in full, and the archive is named in the same
	// breath, so the thing being confirmed and the thing being typed are
	// both on screen. A prompt that says only "are you sure?" is a prompt
	// that gets a reflexive yes.
	warning := t.Err.Render(comp.Truncate(fmt.Sprintf(
		"Restoring %s replaces this world. The current one is archived first and put back if it fails.",
		archive.Name), f.Width))

	// The label gives way before the field does. A prompt that has scrolled
	// its own input off the right-hand edge is unusable, whereas a shorter
	// question is merely terser.
	label := "Type the server name (" + srv.Name + ") to confirm, esc to cancel: "
	if comp.Width(label)+minField > f.Width {
		label = "Type " + srv.Name + " to confirm: "
	}
	field := f.Width - comp.Width(label)
	if field > maxField {
		field = maxField
	}
	if field < 1 {
		// Nothing sensible fits. Say what is being asked for and let the
		// keystrokes land; the panel above still names the archive.
		return warning + "\n" + t.Dim.Render(comp.Truncate("Type "+srv.Name+" to confirm", f.Width))
	}

	prompt := t.Dim.Render(label)
	input := comp.Input{Value: v.typed, Cursor: v.caret, Width: field, Theme: t}.Render()

	return warning + "\n" + prompt + input
}

// Capturing is true while a restore is waiting for the server's name, which
// is free text containing letters the shell binds.
func (v *View) Capturing() bool { return v.confirming }
