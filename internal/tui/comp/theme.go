package comp

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/camden-brown/garrison/internal/model"
)

// Theme carries every colour and glyph decision in one place, so a view never
// writes an escape sequence and the whole interface can be pinned for a test
// or flattened for a 16-colour terminal.
type Theme struct {
	// ASCII replaces box drawing and block elements with plain characters.
	ASCII bool

	Title    lipgloss.Style
	Header   lipgloss.Style
	Dim      lipgloss.Style
	Accent   lipgloss.Style
	Selected lipgloss.Style
	Bar      lipgloss.Style
	Err      lipgloss.Style
}

// Six hues, each with one job. Amber is the interface accent — focus and
// selection — and is never a status, so a highlighted row cannot be mistaken
// for a warning.
const (
	colGreen   = lipgloss.Color("2")
	colAmber   = lipgloss.Color("3")
	colRed     = lipgloss.Color("1")
	colGrey    = lipgloss.Color("8")
	colMagenta = lipgloss.Color("5")
	colCyan    = lipgloss.Color("6")
)

// NewTheme builds the theme for the current terminal.
func NewTheme(ascii bool) *Theme {
	return &Theme{
		ASCII:    ascii,
		Title:    lipgloss.NewStyle().Bold(true),
		Header:   lipgloss.NewStyle().Foreground(colGrey),
		Dim:      lipgloss.NewStyle().Foreground(colGrey),
		Accent:   lipgloss.NewStyle().Foreground(colAmber),
		Selected: lipgloss.NewStyle().Foreground(colAmber).Bold(true),
		Bar:      lipgloss.NewStyle().Foreground(colCyan),
		Err:      lipgloss.NewStyle().Foreground(colRed),
	}
}

// ApplyColorProfile honours GARRISON_COLOR, which overrides lipgloss's own
// detection.
//
// The override exists because detection is wrong often enough on Windows to
// matter: a terminal that reports no colour support renders a fleet in flat
// grey, and the operator concludes the tool is broken rather than that the
// probe missed.
func ApplyColorProfile(value string) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "truecolor", "24bit":
		lipgloss.SetColorProfile(termenv.TrueColor)
	case "256":
		lipgloss.SetColorProfile(termenv.ANSI256)
	case "16", "ansi":
		lipgloss.SetColorProfile(termenv.ANSI)
	case "none", "off", "mono":
		lipgloss.SetColorProfile(termenv.Ascii)
	case "":
		// Leave lipgloss's detection alone.
	}
}

// ColorFromEnv applies the profile named by GARRISON_COLOR, if set.
func ColorFromEnv() { ApplyColorProfile(os.Getenv("GARRISON_COLOR")) }

// StateGlyph, StateWord and StateStyle encode state three ways.
//
// A TUI has three levers — glyph, colour and position — and only position is
// reliable. So state is always carried by a glyph *and* a word, never by
// colour alone: at GARRISON_COLOR=none the fleet still reads correctly, and a
// colour-blind operator is never guessing.
func (t *Theme) StateGlyph(s model.State) string {
	if t.ASCII {
		switch s {
		case model.StateRunning:
			return "*"
		case model.StateRestarting, model.StateCreated:
			return "~"
		case model.StateStopped:
			return "o"
		case model.StateCrashed:
			return "x"
		}
		return "?"
	}

	switch s {
	case model.StateRunning:
		return "●"
	case model.StateRestarting, model.StateCreated:
		return "◐"
	case model.StateStopped:
		return "○"
	case model.StateCrashed:
		return "✕"
	}
	return "?"
}

// StateWord is the state in words. It is always rendered alongside the glyph.
func (t *Theme) StateWord(s model.State) string { return s.String() }

// StateStyle is the colour for a state. Green, red, grey and magenta belong to
// statuses and to nothing else.
func (t *Theme) StateStyle(s model.State) lipgloss.Style {
	switch s {
	case model.StateRunning:
		return lipgloss.NewStyle().Foreground(colGreen)
	case model.StateRestarting, model.StateCreated:
		return lipgloss.NewStyle().Foreground(colAmber)
	case model.StateCrashed:
		return lipgloss.NewStyle().Foreground(colRed)
	case model.StateStopped:
		return lipgloss.NewStyle().Foreground(colGrey)
	}
	// Unknown is magenta: an honest state of its own, never dressed up as
	// stopped.
	return lipgloss.NewStyle().Foreground(colMagenta)
}
