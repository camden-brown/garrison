package comp_test

import (
	"strings"
	"testing"

	"github.com/camden-brown/garrison/internal/tui/comp"
)

func render(lines []comp.DiffLine) string {
	var b strings.Builder
	for _, l := range lines {
		switch l.Op {
		case comp.DiffAdd:
			b.WriteString("+" + l.Text + "\n")
		case comp.DiffRemove:
			b.WriteString("-" + l.Text + "\n")
		default:
			b.WriteString(" " + l.Text + "\n")
		}
	}
	return b.String()
}

func TestDiffReportsNoChange(t *testing.T) {
	same := []string{"a=1", "b=2"}
	lines := comp.DiffLines(same, same)

	if comp.DiffChanged(lines) {
		t.Errorf("identical files reported a change:\n%s", render(lines))
	}
	if len(lines) != 2 {
		t.Errorf("got %d lines, want 2 kept", len(lines))
	}
}

func TestDiffFindsAChangedValue(t *testing.T) {
	lines := comp.DiffLines(
		[]string{"MaxPlayers=8", "PVP=false"},
		[]string{"MaxPlayers=16", "PVP=false"},
	)

	if !comp.DiffChanged(lines) {
		t.Fatal("a changed value reported no change")
	}
	got := render(lines)
	if !strings.Contains(got, "-MaxPlayers=8") || !strings.Contains(got, "+MaxPlayers=16") {
		t.Errorf("diff does not show the change:\n%s", got)
	}
	if strings.Contains(got, "-PVP") || strings.Contains(got, "+PVP") {
		t.Errorf("an unchanged line was reported as changed:\n%s", got)
	}
}

// The case a positional compare gets wrong, and the reason this is a real LCS:
// one line inserted near the top must not report every line below it as
// changed. "47 lines changed" before an apply teaches nobody anything.
func TestDiffSurvivesAnInsertion(t *testing.T) {
	before := []string{"a=1", "b=2", "c=3", "d=4"}
	after := []string{"a=1", "inserted=true", "b=2", "c=3", "d=4"}

	lines := comp.DiffLines(before, after)

	adds, removes := 0, 0
	for _, l := range lines {
		switch l.Op {
		case comp.DiffAdd:
			adds++
		case comp.DiffRemove:
			removes++
		}
	}
	if adds != 1 || removes != 0 {
		t.Errorf("an insertion produced %d adds and %d removes, want 1 and 0:\n%s",
			adds, removes, render(lines))
	}
}

func TestDiffHandlesEmptySides(t *testing.T) {
	created := comp.DiffLines(nil, []string{"a=1", "b=2"})
	if !comp.DiffChanged(created) {
		t.Error("a new file reported no change")
	}
	for _, l := range created {
		if l.Op != comp.DiffAdd {
			t.Errorf("a new file has a %v line, want all additions", l.Op)
		}
	}

	removed := comp.DiffLines([]string{"a=1"}, nil)
	for _, l := range removed {
		if l.Op != comp.DiffRemove {
			t.Errorf("a deleted file has a %v line, want all removals", l.Op)
		}
	}

	if comp.DiffChanged(comp.DiffLines(nil, nil)) {
		t.Error("two empty files reported a change")
	}
}

// A config file is mostly unchanged on any apply. A diff you must scroll past
// forty identical lines to read is one that gets skipped.
func TestDiffContextElidesUnchangedRuns(t *testing.T) {
	var before, after []string
	for i := 0; i < 40; i++ {
		before = append(before, "same")
		after = append(after, "same")
	}
	before = append(before, "old")
	after = append(after, "new")

	full := comp.DiffLines(before, after)
	trimmed := comp.DiffContext(full, 2)

	if len(trimmed) >= len(full) {
		t.Errorf("context trimming kept %d of %d lines", len(trimmed), len(full))
	}
	got := render(trimmed)
	if !strings.Contains(got, "-old") || !strings.Contains(got, "+new") {
		t.Errorf("trimming dropped the actual change:\n%s", got)
	}
}

func TestDiffContextKeepsEverythingWhenSmall(t *testing.T) {
	full := comp.DiffLines([]string{"a"}, []string{"b"})
	if got := comp.DiffContext(full, 3); len(got) != len(full) {
		t.Errorf("trimming a tiny diff changed it: %d -> %d", len(full), len(got))
	}
}
