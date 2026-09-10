package tui

import (
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/tui/comp"
)

// verb is one thing the command line can do.
//
// The set is deliberately the same as the keys: a command line that can do
// things no key can becomes the only way to do them, and then the keys are
// decoration. What it adds is naming a server other than the selected one,
// which is the thing a key cannot express.
type verb struct {
	name string
	op   core.Op
	help string
}

var verbs = []verb{
	{"start", core.OpStart, "start a server"},
	{"stop", core.OpStop, "stop a server"},
	{"restart", core.OpRestart, "restart a server"},
	{"backup", core.OpBackup, "archive a server's world"},
	{"update", core.OpUpdate, "pull the image and recreate"},
}

// commandKey handles a keystroke while the command line is open.
func (a *App) commandKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		a.closeCommand()
		return a, nil

	case tea.KeyEnter:
		line := a.command
		a.closeCommand()
		return a, a.runCommand(line)

	case tea.KeyTab:
		// Completion only extends what is there. Cycling through
		// possibilities on a line that can stop a server is a way to stop
		// the wrong one.
		if done, ok := completeCommand(a.command, a.serverNames()); ok {
			a.command, a.caret = done, len([]rune(done))
		}
		return a, nil
	}

	if text, caret, handled := comp.EditKey(a.command, a.caret, msg); handled {
		a.command, a.caret = text, caret
	}
	return a, nil
}

func (a *App) closeCommand() {
	a.commanding, a.command, a.caret = false, "", 0
}

// runCommand parses a line and dispatches it.
//
// Every failure is a notice rather than a silent no-op. A command line that
// swallows a typo is one you cannot trust with a verb that stops things.
func (a *App) runCommand(line string) tea.Cmd {
	fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), ":"))
	if len(fields) == 0 {
		return nil
	}

	name := strings.ToLower(fields[0])
	server := a.selected
	if len(fields) > 1 {
		server = fields[1]
	}

	for _, v := range verbs {
		if v.name != name {
			continue
		}
		if server == "" {
			a.store.Notify(a.ctx, "", "\""+name+"\" needs a server: pick one, or say \""+name+" <server>\".")
			return nil
		}
		if _, ok := a.snap.Server(server); !ok {
			a.store.Notify(a.ctx, "", "no server called \""+server+"\".")
			return nil
		}
		return Action(v.op, server)
	}

	a.store.Notify(a.ctx, "", "no command \""+name+"\". Known: "+strings.Join(verbNames(), ", ")+".")
	return nil
}

func verbNames() []string {
	out := make([]string, 0, len(verbs))
	for _, v := range verbs {
		out = append(out, v.name)
	}
	return out
}

// serverNames is what tab completion can complete to.
func (a *App) serverNames() []string {
	out := make([]string, 0, len(a.snap.Servers))
	for _, s := range a.snap.Servers {
		out = append(out, s.Name)
	}
	sort.Strings(out)
	return out
}

// completeCommand extends a partial line to the longest unambiguous match,
// reporting whether it changed anything.
//
// The first word completes against the verbs and the second against the
// fleet, because those are the only two positions that exist.
func completeCommand(line string, servers []string) (string, bool) {
	trailing := strings.HasSuffix(line, " ")
	fields := strings.Fields(line)

	switch {
	case len(fields) == 0:
		return line, false

	case len(fields) == 1 && !trailing:
		if match, ok := longestPrefix(verbNames(), fields[0]); ok {
			return match + " ", true
		}
		return line, false

	case len(fields) == 1 && trailing:
		return line, false

	default:
		if match, ok := longestPrefix(servers, fields[len(fields)-1]); ok {
			return strings.Join(append(fields[:len(fields)-1], match), " "), true
		}
		return line, false
	}
}

// longestPrefix is the longest string every candidate starting with prefix
// agrees on. One candidate completes fully; several complete as far as they
// are identical, which is what makes tab useful rather than surprising.
func longestPrefix(candidates []string, prefix string) (string, bool) {
	var hits []string
	for _, c := range candidates {
		if strings.HasPrefix(c, prefix) {
			hits = append(hits, c)
		}
	}
	if len(hits) == 0 {
		return "", false
	}

	out := hits[0]
	for _, h := range hits[1:] {
		for !strings.HasPrefix(h, out) {
			out = out[:len(out)-1]
		}
	}
	if out == prefix {
		return "", false
	}
	return out, true
}

// commandLine renders the prompt, or empty when it is closed.
func (a *App) commandLine(width int) string {
	if !a.commanding {
		return ""
	}
	t := a.theme

	label := ":"

	// An empty line names what it takes. Once there is anything typed the
	// hint gets out of the way, because by then you know.
	hint := ""
	if a.command == "" {
		hint = "  " + strings.Join(verbNames(), " · ") + " · tab completes"
	}

	field := width - comp.Width(label) - comp.Width(hint)
	if field < 1 {
		field, hint = width-comp.Width(label), ""
	}
	if field < 1 {
		return ""
	}
	return t.Accent.Render(label) + comp.Input{
		Value: a.command, Cursor: a.caret, Width: field, Theme: t,
	}.Render() + t.Dim.Render(hint)
}
