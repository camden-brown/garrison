package comp_test

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/camden-brown/garrison/internal/tui/comp"
)

func runes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func typeInto(value string, cursor int, msgs ...tea.KeyMsg) (string, int) {
	for _, m := range msgs {
		value, cursor, _ = comp.EditKey(value, cursor, m)
	}
	return value, cursor
}

func TestEditKeyTypes(t *testing.T) {
	got, cursor := typeInto("", 0, runes("H"), runes("u"), runes("l"))
	if got != "Hul" {
		t.Errorf("value = %q, want %q", got, "Hul")
	}
	if cursor != 3 {
		t.Errorf("cursor = %d, want 3", cursor)
	}
}

// A server name with a space in it is the common case, and space arrives as
// its own key type rather than in Runes.
func TestEditKeyAcceptsSpace(t *testing.T) {
	got, _ := typeInto("My", 2, tea.KeyMsg{Type: tea.KeySpace}, runes("S"))
	if got != "My S" {
		t.Errorf("value = %q, want %q", got, "My S")
	}
}

func TestEditKeyInsertsAtTheCursor(t *testing.T) {
	got, cursor := typeInto("Hulra", 3, runes("d"))
	if got != "Huldra" {
		t.Errorf("value = %q, want %q", got, "Huldra")
	}
	if cursor != 4 {
		t.Errorf("cursor = %d, want 4", cursor)
	}
}

func TestEditKeyDeletes(t *testing.T) {
	got, cursor := typeInto("Huldra", 6, tea.KeyMsg{Type: tea.KeyBackspace})
	if got != "Huldr" || cursor != 5 {
		t.Errorf("backspace gave %q at %d, want %q at 5", got, cursor, "Huldr")
	}

	got, cursor = typeInto("Huldra", 0, tea.KeyMsg{Type: tea.KeyDelete})
	if got != "uldra" || cursor != 0 {
		t.Errorf("delete gave %q at %d, want %q at 0", got, cursor, "uldra")
	}
}

// Deleting at the edges is where an off-by-one turns into a panic on a slice
// bound, so both ends are pinned.
func TestEditKeyIsSafeAtTheEdges(t *testing.T) {
	if got, cursor := typeInto("", 0, tea.KeyMsg{Type: tea.KeyBackspace}); got != "" || cursor != 0 {
		t.Errorf("backspace on empty gave %q at %d", got, cursor)
	}
	if got, cursor := typeInto("ab", 2, tea.KeyMsg{Type: tea.KeyDelete}); got != "ab" || cursor != 2 {
		t.Errorf("delete at end gave %q at %d", got, cursor)
	}
	if _, cursor := typeInto("ab", 0, tea.KeyMsg{Type: tea.KeyLeft}); cursor != 0 {
		t.Errorf("left at start gave cursor %d, want 0", cursor)
	}
	if _, cursor := typeInto("ab", 2, tea.KeyMsg{Type: tea.KeyRight}); cursor != 2 {
		t.Errorf("right at end gave cursor %d, want 2", cursor)
	}
}

// A cursor from a stale render must not index out of a shorter value.
func TestEditKeyClampsACursorPastTheEnd(t *testing.T) {
	got, cursor := typeInto("ab", 99, runes("c"))
	if got != "abc" || cursor != 3 {
		t.Errorf("value = %q at %d, want %q at 3", got, cursor, "abc")
	}
}

func TestEditKeyLeavesUnknownKeysAlone(t *testing.T) {
	value, cursor, handled := comp.EditKey("ab", 1, tea.KeyMsg{Type: tea.KeyEsc})
	if handled {
		t.Error("esc was consumed, want it left for the caller")
	}
	if value != "ab" || cursor != 1 {
		t.Errorf("esc changed state to %q at %d", value, cursor)
	}
}

func TestRenderOccupiesExactlyItsWidth(t *testing.T) {
	for _, value := range []string{"", "Huldra", "a much longer value than the field", "日本語のワールド"} {
		in := comp.Input{Value: value, Cursor: len([]rune(value)), Width: 12, Theme: comp.NewTheme(false)}
		if w := comp.Width(in.Render()); w != 12 {
			t.Errorf("Render(%q) is %d cells, want 12", value, w)
		}
	}
}

// The cursor going off the end of a long value is what makes a field feel
// broken: you type and nothing appears to change.
func TestRenderScrollsToKeepTheCursorVisible(t *testing.T) {
	value := "abcdefghijklmnopqrstuvwxyz"
	in := comp.Input{Value: value, Cursor: len([]rune(value)), Width: 8, Theme: comp.NewTheme(false)}

	got := in.Render()
	if !strings.Contains(got, "z") {
		t.Errorf("Render() = %q, want the end of the value visible", got)
	}
	if strings.Contains(got, "a") {
		t.Errorf("Render() = %q, want the start scrolled off", got)
	}
}

func TestRenderIsEmptyWithNoWidth(t *testing.T) {
	if got := (comp.Input{Value: "x", Width: 0, Theme: comp.NewTheme(false)}).Render(); got != "" {
		t.Errorf("Render() = %q, want empty", got)
	}
}
