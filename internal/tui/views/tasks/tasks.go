// Package tasks is the Tasks view: the running task expanded to its steps,
// with what has finished beneath it.
//
// A task engine you cannot watch is not one you should automate, which is why
// DESIGN puts this screen in the same milestone as the engine rather than
// after the scheduler.
package tasks

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/model"
	engine "github.com/camden-brown/garrison/internal/tasks"
	"github.com/camden-brown/garrison/internal/tui"
	"github.com/camden-brown/garrison/internal/tui/comp"

	"github.com/charmbracelet/lipgloss"
)

// lipglossStyle is a local alias so the helpers read without importing
// lipgloss for one type name.
type lipglossStyle = lipgloss.Style

// View lists tasks and expands the selected one to its steps.
type View struct {
	cursor int
	// all shows every server's tasks rather than the selected server's.
	// A lane is per server, but a fleet-wide view is how you notice that
	// three of them are queued behind one slow update.
	all bool
}

func New() *View { return &View{} }

func (v *View) ID() tui.ViewID { return tui.ViewTasks }
func (v *View) Title() string  { return "Tasks" }

func (v *View) Available(model.Instance) (bool, string) { return true, "" }

var (
	keyUp     = key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up"))
	keyDown   = key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down"))
	keyAll    = key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "all servers"))
	keyCancel = key.NewBinding(key.WithKeys("C"), key.WithHelp("C", "cancel task"))
)

func (v *View) Keys() []key.Binding { return []key.Binding{keyUp, keyDown, keyAll, keyCancel} }

func (v *View) Update(msg tea.Msg, f tui.Frame, snap core.Snapshot) (tui.View, tea.Cmd) {
	msgKey, ok := msg.(tea.KeyMsg)
	if !ok {
		return v, nil
	}

	next := *v
	list := next.list(f, snap)

	switch {
	case key.Matches(msgKey, keyUp):
		next.cursor--
	case key.Matches(msgKey, keyDown):
		next.cursor++
	case key.Matches(msgKey, keyAll):
		next.all = !next.all
		next.cursor = 0
	case key.Matches(msgKey, keyCancel):
		if t, ok := at(list, next.cursor); ok && !t.State.Done() {
			return &next, tui.CancelTask(t.ID)
		}
	}
	next.clamp(len(list))
	return &next, nil
}

func (v *View) clamp(n int) {
	if v.cursor >= n {
		v.cursor = n - 1
	}
	if v.cursor < 0 {
		v.cursor = 0
	}
}

// list is what the screen shows, newest first — the running ones matter most
// and a finished task moving down the list as new ones arrive is what a
// history should do.
func (v *View) list(f tui.Frame, snap core.Snapshot) []engine.Progress {
	out := make([]engine.Progress, 0, len(snap.Tasks))
	for i := len(snap.Tasks) - 1; i >= 0; i-- {
		t := snap.Tasks[i]
		if !v.all && f.Server != "" && t.Server != f.Server {
			continue
		}
		out = append(out, t)
	}
	return out
}

func at(list []engine.Progress, i int) (engine.Progress, bool) {
	if i < 0 || i >= len(list) {
		return engine.Progress{}, false
	}
	return list[i], true
}

func (v *View) Render(f tui.Frame, snap core.Snapshot) string {
	t := f.Theme
	list := v.list(f, snap)
	v.clamp(len(list))

	scope := "this server"
	if v.all || f.Server == "" {
		scope = "all servers"
	}

	var b strings.Builder
	b.WriteString(comp.Panel{
		Theme: t, Title: "TASKS", Right: scope + " · a scope · C cancel",
		Width: f.Width, Focused: f.Focused,
	}.Render(v.rows(t, list, comp.Inner(f.Width))))

	if current, ok := at(list, v.cursor); ok {
		b.WriteString("\n")
		b.WriteString(comp.Panel{
			Theme: t, Title: strings.ToUpper(string(current.Kind)), Right: current.Server,
			Width: f.Width,
		}.Render(steps(t, current, comp.Inner(f.Width))))
	}
	return b.String()
}

func (v *View) rows(t *comp.Theme, list []engine.Progress, width int) string {
	if len(list) == 0 {
		return t.Dim.Render("Nothing has run yet.")
	}

	var b strings.Builder
	for i, task := range list {
		selected := i == v.cursor

		marker := "  "
		if selected {
			marker = "▌ "
			if t.ASCII {
				marker = "> "
			}
		}

		kind := comp.Pad(string(task.Kind), 14)
		server := comp.Pad(task.Server, 18)
		state := stateCell(t, task, selected)
		detail := comp.Pad(rowDetail(task), width-2-14-18-12-1)

		b.WriteString(t.On(t.Accent, selected).Render(marker))
		b.WriteString(t.On(t.Title, selected).Render(kind))
		b.WriteString(t.On(t.Dim, selected).Render(server))
		b.WriteString(state)
		b.WriteString(t.On(t.Dim, selected).Render(" " + detail))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// rowDetail is the one thing worth knowing about a task at a glance: where a
// running one has got to, why a queued one is waiting, or what went wrong.
func rowDetail(task engine.Progress) string {
	switch task.State {
	case engine.StateQueued:
		return task.Waiting
	case engine.StateRunning:
		return fmt.Sprintf("step %d/%d · %s", task.Cursor+1, len(task.Steps), task.StepName())
	case engine.StateDone:
		return "took " + comp.Duration(task.Ended.Sub(task.Started))
	}
	return task.Err
}

func stateCell(t *comp.Theme, task engine.Progress, selected bool) string {
	style := t.Dim
	switch task.State {
	case engine.StateRunning:
		style = t.Accent
	case engine.StateDone:
		style = t.StateStyle(model.StateRunning)
	case engine.StateFailed, engine.StateRolledBack:
		style = t.Err
	}
	return t.On(style, selected).Render(comp.Pad(task.State.String(), 12))
}

// steps is the selected task expanded: what has run, what is running, what is
// still to come, and how long each was expected to take.
//
// Showing the whole sequence rather than only the current step is the
// difference between a progress bar and an explanation. When a restart is
// sitting on "stop" for ninety seconds, the thing you want to know is that
// there are four steps after it.
func steps(t *comp.Theme, task engine.Progress, width int) string {
	if len(task.Steps) == 0 {
		return t.Dim.Render("No steps recorded.")
	}

	var b strings.Builder
	for i, name := range task.Steps {
		glyph, style := stepGlyph(t, task, i)

		est := ""
		if i < len(task.Est) && task.Est[i] > 0 {
			est = "~" + comp.Duration(task.Est[i])
		}

		b.WriteString(style.Render(glyph + " " + comp.Pad(name, 28)))
		b.WriteString(t.Dim.Render(est))
		b.WriteString("\n")
	}

	if len(task.History) > 0 {
		b.WriteString("\n")
		for _, line := range task.History {
			b.WriteString(t.Dim.Render(comp.Truncate("  "+line, width)))
			b.WriteString("\n")
		}
	}
	if task.Err != "" {
		b.WriteString("\n")
		b.WriteString(t.Err.Render(comp.Truncate(task.Err, width)))
	}
	return strings.TrimRight(b.String(), "\n")
}

func stepGlyph(t *comp.Theme, task engine.Progress, i int) (string, lipglossStyle) {
	done, running, pending := "✓", "▸", "·"
	if t.ASCII {
		done, running, pending = "x", ">", "."
	}

	switch {
	case i < task.Cursor:
		return done, t.StateStyle(model.StateRunning)
	case i == task.Cursor && task.State == engine.StateRunning:
		return running, t.Accent
	case i == task.Cursor && (task.State == engine.StateFailed || task.State == engine.StateRolledBack):
		return "✕", t.Err
	case task.State == engine.StateDone:
		return done, t.StateStyle(model.StateRunning)
	}
	return pending, t.Dim
}
