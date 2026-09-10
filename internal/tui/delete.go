package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/tui/comp"
)

// Deleting a server, and stepping between them.
//
// X is the last of the three places DESIGN puts deliberate friction, and the
// only one where the friction is the entire feature: the task behind it is
// three steps and none of them are clever. What makes it safe to bind to a
// single key is that the name has to be typed and that the world is not
// touched — everything Garrison made goes, and the one thing it did not stays.

// deleteRows is how much of the stage the confirmation takes.
const deleteRows = 6

// startDelete opens the confirmation, or says why it cannot.
func (a *App) startDelete() {
	srv, ok := a.snap.Server(a.selected)
	if !ok {
		a.store.Notify(a.ctx, "", "pick a server first — X deletes one, not the fleet.")
		return
	}
	if !srv.Configured {
		// A container found by label with no file behind it. Removing it
		// would be a docker rm with extra steps, and Garrison would find it
		// again on the next poll.
		a.store.Notify(a.ctx, srv.Name,
			"Garrison has no configuration for this server, so there is nothing of its own to delete.")
		return
	}
	a.deleting = srv.Name
	a.deleteTyped, a.deleteCaret = "", 0
}

func (a *App) stopDeleting() {
	a.deleting, a.deleteTyped, a.deleteCaret = "", "", 0
}

// deleteKey handles a keystroke while the confirmation is up.
func (a *App) deleteKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		a.stopDeleting()
		return a, nil

	case tea.KeyEnter:
		if a.deleteTyped != a.deleting {
			// A near miss clears rather than sitting one keystroke away
			// from deleting the wrong thing.
			a.deleteTyped, a.deleteCaret = "", 0
			return a, nil
		}
		server := a.deleting
		a.stopDeleting()
		return a, Action(core.OpDelete, server)
	}

	// Everything else is text. The name contains letters that are otherwise
	// keys, which is the whole reason this swallows them.
	if typed, caret, handled := comp.EditKey(a.deleteTyped, a.deleteCaret, msg); handled {
		a.deleteTyped, a.deleteCaret = typed, caret
	}
	return a, nil
}

// deleteView renders the confirmation.
func (a *App) deleteView(width int) string {
	t := a.theme

	var data string
	if srv, ok := a.snap.Server(a.deleting); ok && srv.Instance.Data != "" {
		data = srv.Instance.Data
	}

	var b strings.Builder
	b.WriteString(t.Err.Render(comp.Truncate(
		"Delete "+a.deleting+": its container and its configuration go.", comp.Inner(width))))
	b.WriteString("\n")

	// Saying what survives is the more useful half. It is also the sentence
	// that makes the key safe to have at all.
	kept := "There is no data directory to keep."
	if data != "" {
		kept = "The world at " + data + " is left exactly where it is."
	}
	b.WriteString(t.Dim.Render(comp.Truncate(kept, comp.Inner(width))))
	b.WriteString("\n")

	label := "Type " + a.deleting + " to confirm, esc to cancel: "
	field := comp.Inner(width) - comp.Width(label)
	if field > 24 {
		field = 24
	}
	if field < 1 {
		b.WriteString(t.Dim.Render(comp.Truncate(label, comp.Inner(width))))
	} else {
		b.WriteString(t.Dim.Render(label))
		b.WriteString(comp.Input{
			Value: a.deleteTyped, Cursor: a.deleteCaret, Width: field, Theme: t,
		}.Render())
	}

	return comp.Panel{
		Theme: t, Title: "DELETE SERVER", Right: "the world is kept",
		Width: width, Focused: true,
	}.Render(b.String())
}

// stepServer moves the selection along the fleet, keeping the current screen.
//
// The rail's arrow keys do this too, but only while the rail has focus.
// Comparing two servers means looking at the same screen for each in turn,
// and taking focus off the stage to do it loses the scroll position you were
// comparing from.
func (a *App) stepServer(delta int) {
	if len(a.snap.Servers) == 0 {
		return
	}

	at := 0
	for i, srv := range a.snap.Servers {
		if srv.Name == a.selected {
			at = i
			break
		}
	}

	// Wrapping, because a fleet is a ring you cycle rather than a list you
	// fall off the end of.
	n := len(a.snap.Servers)
	a.selected = a.snap.Servers[((at+delta)%n+n)%n].Name
}
