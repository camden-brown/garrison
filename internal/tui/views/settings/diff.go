package settings

import (
	"fmt"
	"strings"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/games"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tui/comp"
)

// The apply diff, generated rather than described.
//
// DESIGN asks for "a literal config-file diff before anything is written", and
// the reason it is worth having is that a settings form is an abstraction over
// files: impact badges tell you what a change costs, and nothing tells you
// what it *says*. A key renamed between game versions, a value quoted
// differently, a field that writes two lines rather than one — all invisible
// until the server refuses to start.
//
// It needs no I/O, which is what makes it possible in a view at all. Compile
// is a pure function from settings to file contents, so the diff is between
// Compile of the instance as saved and Compile of the instance with the draft
// applied. The file on disk never enters into it — deliberately: what an
// operator is deciding about is the change they just made, not the drift
// between Garrison and something that edited the file behind it.

// fileDiff is one file's worth of change.
type fileDiff struct {
	path  string
	lines []comp.DiffLine
}

// applyDiff is what applying the draft would do to the game's config files.
//
// The second return says whether the game writes files at all, which is a
// different answer from "nothing changed" and has to be reported differently:
// Valheim compiles to no files, so every setting is environment and the cost
// is a new container rather than a rewritten line.
func applyDiff(srv core.Server, g games.Game) (diffs []fileDiff, writesFiles bool) {
	compiler, ok := g.(interface {
		Compile(model.Instance) ([]model.File, error)
	})
	if !ok {
		return nil, false
	}

	before, err := compiler.Compile(srv.Instance)
	if err != nil {
		return nil, false
	}
	after, err := compiler.Compile(drafted(srv))
	if err != nil {
		return nil, false
	}
	if len(before) == 0 && len(after) == 0 {
		return nil, false
	}

	byPath := map[string][]string{}
	var order []string
	for _, f := range before {
		if _, seen := byPath[f.Path]; !seen {
			order = append(order, f.Path)
		}
		byPath[f.Path] = splitLines(f.Data)
	}

	newByPath := map[string][]string{}
	for _, f := range after {
		if _, seen := byPath[f.Path]; !seen {
			if _, seen := newByPath[f.Path]; !seen {
				order = append(order, f.Path)
			}
		}
		newByPath[f.Path] = splitLines(f.Data)
	}

	for _, path := range order {
		lines := comp.DiffLines(byPath[path], newByPath[path])
		if !comp.DiffChanged(lines) {
			continue
		}
		diffs = append(diffs, fileDiff{path: path, lines: comp.DiffContext(lines, 2)})
	}
	return diffs, true
}

// drafted is the instance as it would be after applying, which is the input
// the "after" side of the diff is compiled from.
func drafted(srv core.Server) model.Instance {
	out := srv.Instance

	settings := make(map[string]any, len(out.Settings)+len(srv.Draft))
	for k, v := range out.Settings {
		settings[k] = v
	}
	for k, v := range srv.Draft {
		settings[k] = v
	}
	out.Settings = settings
	return out
}

func splitLines(content []byte) []string {
	if len(content) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(content), "\n"), "\n")
}

// renderDiff draws the diff, or the sentence that stands in for one.
func (v *View) renderDiff(t *comp.Theme, srv core.Server, g games.Game, width, rows int) string {
	diffs, writesFiles := applyDiff(srv, g)

	if !writesFiles {
		// Not a failure and worth saying plainly: this game keeps its
		// settings in the container's environment, so there is no file to
		// show a change in and the change costs a new container.
		return t.Dim.Render(comp.Truncate(
			g.Meta().Name+" writes no config files — these settings are the container's "+
				"environment, so applying builds a new one.", width))
	}
	if len(diffs) == 0 {
		return t.Dim.Render(comp.Truncate(
			"No config file changes. The pending edits do not reach a file.", width))
	}

	var b strings.Builder
	written := 0
	for _, d := range diffs {
		if written >= rows {
			break
		}
		b.WriteString(t.Header.Render(comp.Truncate(d.path, width)))
		b.WriteString("\n")
		written++

		for _, line := range d.lines {
			if written >= rows {
				b.WriteString(t.Dim.Render("  …"))
				b.WriteString("\n")
				written++
				break
			}
			b.WriteString(renderDiffLine(t, line, width))
			b.WriteString("\n")
			written++
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderDiffLine(t *comp.Theme, line comp.DiffLine, width int) string {
	switch line.Op {
	case comp.DiffAdd:
		return t.Chat.Render(comp.Truncate("  + "+line.Text, width))
	case comp.DiffRemove:
		return t.Err.Render(comp.Truncate("  - "+line.Text, width))
	}
	if line.Text == "" {
		// An elided run of unchanged lines.
		return t.Dim.Render("  ⋮")
	}
	return t.Dim.Render(comp.Truncate("    "+line.Text, width))
}

// diffRows is how much of the frame the diff panel may take when confirming.
func diffRows(height int) int {
	n := height/2 - 4
	if n < 3 {
		return 3
	}
	if n > 14 {
		return 14
	}
	return n
}

// confirmPrompt is the question, and for a wipe risk the field that answers
// it.
func (v *View) confirmPrompt(t *comp.Theme, srv core.Server, form form, width int) string {
	pending := srv.Pending()
	worst := worstImpact(srv, form)

	if worst < games.ImpactWipeRisk {
		return t.Accent.Render(comp.Truncate(
			fmt.Sprintf("apply %d change%s to %s?  %s  ·  y / n",
				pending, plural(pending), srv.Name, applyCost(worst)), width))
	}

	warning := t.Err.Render(comp.Truncate(
		fmt.Sprintf("%d change%s to %s, and one of them can lose the world. A backup is taken first.",
			pending, plural(pending), srv.Name), width))

	label := "Type the server name (" + srv.Name + ") to confirm, esc to cancel: "
	if comp.Width(label)+20 > width {
		label = "Type " + srv.Name + " to confirm: "
	}
	field := width - comp.Width(label)
	if field > 24 {
		field = 24
	}
	if field < 1 {
		return warning + "\n" + t.Dim.Render(comp.Truncate("Type "+srv.Name+" to confirm", width))
	}

	return warning + "\n" + t.Dim.Render(label) +
		comp.Input{Value: v.typed, Cursor: v.caret, Width: field, Theme: t}.Render()
}
