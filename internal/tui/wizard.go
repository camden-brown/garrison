package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/camden-brown/garrison/internal/games"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tui/comp"
)

// The provisioner: "n" turns a game and a name into a configured server.
//
// It lives on the shell rather than behind the View contract because it is not
// about the selected server — it is how a server comes to exist, and a screen
// that belongs to a server cannot be the place you make one.
//
// What it will not do is guess at the interesting parts. Ports are proposed
// from the live fleet rather than from the game's defaults alone, because the
// second Valheim server on a machine collides with the first and finding that
// out from a container that will not start is a bad way to learn it.

// wizardField is one row of the form.
type wizardField uint8

const (
	fieldGame wizardField = iota
	fieldName
	fieldData
	fieldCount
)

// wizardState is the form's contents. Shell state, like the command line: an
// abandoned half-filled form is not worth preserving across a resize.
type wizardState struct {
	open  bool
	field wizardField

	game int // index into games.All()
	name string
	data string
	// caret is the edit position in whichever text field has focus.
	caret int
}

func (a *App) openWizard() {
	all := games.All()
	if len(all) == 0 {
		a.store.Notify(a.ctx, "", "no game plugins are compiled in, so there is nothing to provision.")
		return
	}
	a.wizard = wizardState{open: true}
}

func (a *App) wizardKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	w := a.wizard
	all := games.All()

	switch msg.Type {
	case tea.KeyEsc:
		a.wizard = wizardState{}
		return a, nil

	case tea.KeyTab, tea.KeyDown:
		w.field = (w.field + 1) % fieldCount
		w.caret = len([]rune(a.wizardText(w, w.field)))
		a.wizard = w
		return a, nil

	case tea.KeyShiftTab, tea.KeyUp:
		w.field = (w.field + fieldCount - 1) % fieldCount
		w.caret = len([]rune(a.wizardText(w, w.field)))
		a.wizard = w
		return a, nil

	case tea.KeyEnter:
		inst, err := a.wizardInstance(w)
		if err != nil {
			a.store.Notify(a.ctx, "", err.Error())
			return a, nil
		}
		a.wizard = wizardState{}
		a.store.CreateServer(a.ctx, inst)
		a.selected = inst.Name
		return a, nil
	}

	// The game field cycles; the rest take text.
	if w.field == fieldGame {
		switch msg.String() {
		case "left", "h":
			w.game = (w.game + len(all) - 1) % len(all)
		case "right", "l", " ":
			w.game = (w.game + 1) % len(all)
		}
		a.wizard = w
		return a, nil
	}

	text, caret, handled := comp.EditKey(a.wizardText(w, w.field), w.caret, msg)
	if !handled {
		return a, nil
	}
	switch w.field {
	case fieldName:
		w.name = text
	case fieldData:
		w.data = text
	}
	w.caret = caret
	a.wizard = w
	return a, nil
}

// wizardText is a field's current value, with the defaults that make the form
// mostly a matter of pressing enter.
func (a *App) wizardText(w wizardState, f wizardField) string {
	switch f {
	case fieldName:
		return w.name
	case fieldData:
		if w.data != "" {
			return w.data
		}
		return a.defaultData(w)
	}
	return ""
}

// defaultData is where a new server's world goes when nobody says otherwise:
// beside the servers that already exist, named for the instance.
func (a *App) defaultData(w wizardState) string {
	name := w.name
	if name == "" {
		name = "<name>"
	}
	for _, srv := range a.snap.Servers {
		if srv.Instance.Data != "" {
			return filepath.Join(filepath.Dir(srv.Instance.Data), name)
		}
	}
	return name
}

// wizardInstance turns the form into an instance, or says what is missing.
func (a *App) wizardInstance(w wizardState) (model.Instance, error) {
	all := games.All()
	if len(all) == 0 {
		return model.Instance{}, fmt.Errorf("no game plugins are compiled in")
	}
	g := all[min(w.game, len(all)-1)]

	name := strings.TrimSpace(w.name)
	if name == "" {
		return model.Instance{}, fmt.Errorf("a server needs a name")
	}
	if strings.ContainsAny(name, ` /\:*?"<>|`) {
		// The name becomes a file name and a container name, so the
		// characters neither of those will take are refused here rather
		// than surfacing as a Docker error later.
		return model.Instance{}, fmt.Errorf("%q cannot be a server name: no spaces or /\\:*?\"<>|", name)
	}
	if _, exists := a.snap.Server(name); exists {
		return model.Instance{}, fmt.Errorf("there is already a server called %q", name)
	}

	data := strings.TrimSpace(w.data)
	if data == "" {
		data = a.defaultData(wizardState{name: name})
	}

	return model.Instance{
		Name:  name,
		Game:  g.Meta().ID,
		Image: g.Meta().DefaultImage,
		Data:  data,
		Ports: a.proposePorts(g),
	}, nil
}

// proposePorts shifts a game's default ports past anything the fleet is
// already using.
//
// Scanning the live fleet rather than trying to bind is the difference between
// "your second Valheim server is on 2458" and a container that exits because
// 2456 was taken. The whole block moves together, because a game that wants
// consecutive ports usually means it.
func (a *App) proposePorts(g games.Game) []model.PortMap {
	defaults := g.Meta().DefaultPorts
	if len(defaults) == 0 {
		return nil
	}

	used := map[int]bool{}
	for _, srv := range a.snap.Servers {
		for _, p := range srv.Ports {
			used[p.Host] = true
		}
		for _, p := range srv.Instance.Ports {
			used[p.Host] = true
		}
	}

	for shift := 0; shift < 200; shift++ {
		out := make([]model.PortMap, 0, len(defaults))
		clash := false
		for _, p := range defaults {
			host := p.Host + shift
			if used[host] {
				clash = true
				break
			}
			out = append(out, model.PortMap{Container: p.Container, Host: host})
		}
		if !clash {
			return out
		}
	}
	// Nothing free in two hundred tries is a machine with a problem the
	// wizard cannot solve. Hand back the defaults and let the create fail
	// with Docker's own message, which will be more specific than ours.
	return defaults
}

// wizardView renders the form.
func (a *App) wizardView(width int) string {
	t := a.theme
	w := a.wizard
	all := games.All()

	gameName := "none"
	if len(all) > 0 {
		gameName = all[min(w.game, len(all)-1)].Meta().Name
	}

	rows := []struct {
		field wizardField
		label string
		value string
		hint  string
	}{
		{fieldGame, "Game", gameName, "← → to change"},
		{fieldName, "Name", w.name, "the instance and the container name"},
		{fieldData, "World data", a.wizardText(w, fieldData), "where the save lives"},
	}

	var b strings.Builder
	for _, r := range rows {
		selected := r.field == w.field

		marker := "  "
		if selected {
			marker = "▌ "
			if t.ASCII {
				marker = "> "
			}
		}
		b.WriteString(t.On(t.Accent, selected).Render(marker))
		b.WriteString(t.On(t.Dim, selected).Render(comp.Pad(r.label, 14)))

		if selected && r.field != fieldGame {
			b.WriteString(comp.Input{Value: r.value, Cursor: w.caret, Width: 34, Theme: t}.Render())
		} else {
			b.WriteString(t.On(t.Title, selected).Render(comp.Pad(r.value, 34)))
		}
		b.WriteString(t.Dim.Render(comp.Truncate("  "+r.hint, comp.Inner(width)-52)))
		b.WriteString("\n")
	}

	// The ports are the part the wizard is actually for, so they are shown
	// rather than left to be discovered in the TOML.
	if len(all) > 0 {
		if ports := a.proposePorts(all[min(w.game, len(all)-1)]); len(ports) > 0 {
			var names []string
			for _, p := range ports {
				names = append(names, fmt.Sprintf("%d→%s", p.Host, p.Container))
			}
			b.WriteString(t.Dim.Render("  Ports         " + strings.Join(names, "  ") + "   (proposed from the live fleet)"))
			b.WriteString("\n")
		}
	}

	return comp.Panel{
		Theme: t, Title: "NEW SERVER", Right: "enter creates · esc cancels",
		Width: width, Focused: true,
	}.Render(strings.TrimRight(b.String(), "\n"))
}

// wizardRows is how much of the stage the form takes.
const wizardRows = 7
