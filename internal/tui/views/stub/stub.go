// Package stub is the screen a view shows before it is built.
//
// It exists so the rail can be complete from the start. A navigation model you
// can only half use is hard to judge, and an entry that leads to a blank pane
// is indistinguishable from one that is broken. Each stub says what the screen
// will be, what it is waiting on, and which milestone brings it.
package stub

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tui"
	"github.com/camden-brown/garrison/internal/tui/comp"
)

// View is a placeholder screen.
type View struct {
	id        tui.ViewID
	title     string
	milestone string
	summary   string
	// blocked, when non-empty, is why the screen cannot work yet even in
	// principle — a capability the game does not have, rather than code
	// that has not been written.
	blocked func(model.Instance) string
}

// New returns a stub. Milestone is the one that replaces it.
func New(id tui.ViewID, title, milestone, summary string) *View {
	return &View{id: id, title: title, milestone: milestone, summary: summary}
}

func (v *View) ID() tui.ViewID      { return v.id }
func (v *View) Title() string       { return v.title }
func (v *View) Keys() []key.Binding { return nil }

// Available reports the stub as usable so the rail still routes to it. The
// explanation lives in the body rather than in a refusal: an entry you cannot
// open tells you less than one that opens and explains itself.
func (v *View) Available(inst model.Instance) (bool, string) {
	if v.blocked != nil {
		if reason := v.blocked(inst); reason != "" {
			return false, reason
		}
	}
	return true, ""
}

func (v *View) Update(tea.Msg, tui.Frame, core.Snapshot) (tui.View, tea.Cmd) { return v, nil }

func (v *View) Render(f tui.Frame, snap core.Snapshot) string {
	t := f.Theme

	var b strings.Builder
	b.WriteString(t.Title.Render(strings.ToUpper(v.title)))
	if f.Server != "" {
		b.WriteString("  ")
		b.WriteString(t.Dim.Render(f.Server))
	}
	b.WriteString("\n\n")

	b.WriteString(t.Dim.Render(comp.Truncate(v.summary, f.Width)))
	b.WriteString("\n\n")
	b.WriteString(t.Accent.Render("Arrives in " + v.milestone + "."))
	b.WriteString("\n")
	return b.String()
}

// Capturing is false: a stub handles no keys at all.
func (v *View) Capturing() bool { return false }
