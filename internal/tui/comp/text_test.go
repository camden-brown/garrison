package comp

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// withColour runs f with a real colour profile. The golden tests pin the
// profile to Ascii, which emits no escape sequences at all — so every bug in
// how this code handles them is invisible to the entire rest of the suite.
func withColour(t *testing.T, f func()) {
	t.Helper()
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	f()
}

func dim(s string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(s)
}

// An escape sequence occupies no cells. Counting it as width truncates far too
// early, and cutting through the middle of one leaves its tail on screen as
// literal text — which is where a stray "[38;5;8m" beside a column heading
// comes from.
func TestWidthIgnoresEscapeSequences(t *testing.T) {
	withColour(t, func() {
		styled := dim("hello")
		if got := Width(styled); got != 5 {
			t.Errorf("Width(styled) = %d, want 5 — the colour codes are not content", got)
		}
		if len(styled) <= 5 {
			t.Fatal("the test string carries no escape sequences; it proves nothing")
		}
	})
}

func TestTruncateKeepsStyledTextIntact(t *testing.T) {
	withColour(t, func() {
		styled := dim("a long dim line of text")
		got := Truncate(styled, 10)

		if w := Width(got); w != 10 {
			t.Errorf("visible width = %d, want 10 (%q)", w, got)
		}
		// A truncation that drops the reset leaks the colour onto whatever
		// is printed next; one that cuts mid-sequence prints the remainder.
		if !strings.HasSuffix(got, "\x1b[0m") {
			t.Errorf("truncated string does not end with a reset: %q", got)
		}
	})
}

func TestTruncateDoesNotCutMidSequence(t *testing.T) {
	withColour(t, func() {
		styled := dim("abcdefghijklmnopqrstuvwxyz")

		// Every width, so no boundary can slip through.
		for w := 1; w <= 30; w++ {
			got := Truncate(styled, w)
			// Every "[" must belong to a sequence, which means every one
			// must be preceded by an escape byte. A bare one is the tail of
			// a sequence that was cut in half, and it prints as text.
			for i, r := range got {
				if r == '[' && (i == 0 || got[i-1] != 0x1b) {
					t.Errorf("width %d left a bare '[' at %d: %q", w, i, got)
				}
			}
			if visible := Width(got); visible > w {
				t.Errorf("width %d produced %d visible cells: %q", w, visible, got)
			}
		}
	})
}

// A column that pads a styled string using byte length pads by far too little,
// so the column beside it starts in the wrong place.
func TestPadMeasuresStyledTextCorrectly(t *testing.T) {
	withColour(t, func() {
		for _, s := range []string{dim("cpu"), dim(""), "plain", dim("●") + " running"} {
			padded := Pad(s, 20)
			if got := Width(padded); got != 20 {
				t.Errorf("Pad(%q) is %d cells, want 20", s, got)
			}
		}
		for _, s := range []string{dim("14.2%"), "0"} {
			padded := PadLeft(s, 8)
			if got := Width(padded); got != 8 {
				t.Errorf("PadLeft(%q) is %d cells, want 8", s, got)
			}
		}
	})
}

// The components are given already-styled content by their callers, so their
// own clamping has to survive it too.
func TestPanelAndRailSurviveColour(t *testing.T) {
	withColour(t, func() {
		body := dim(strings.Repeat("a very long styled line ", 20))
		out := Panel{Theme: NewTheme(false), Title: "SERVERS", Right: "keys", Width: 40}.Render(body)

		for i, line := range strings.Split(out, "\n") {
			if w := Width(line); w != 40 {
				t.Errorf("panel line %d is %d cells, want 40", i, w)
			}
		}
	})
}
