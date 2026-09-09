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

	// Chat is magenta and nothing else is, so a conversation separates from
	// server output at a glance — DESIGN §4.
	Chat lipgloss.Style

	// Rule draws the dividers between sections.
	Rule lipgloss.Style

	// Badge is the screen name in the status bar: dark text on an accent
	// block, so the one fixed thing on the display is also the most
	// findable.
	Badge lipgloss.Style

	// Spark is the sparkline colour, cyan by default. A tile may override
	// it — players are green because the number is a good thing.
	Spark lipgloss.Style
}

// SelectionBG is the background for the highlighted row.
//
// It is a colour rather than a style because a selected row is built from many
// differently coloured cells, and every one of them has to carry the
// background itself. Wrapping the finished row in a background style does not
// work: the first reset inside it ends the fill, and the highlight stops
// halfway across.
func (t *Theme) SelectionBG() lipgloss.TerminalColor { return colSelected }

// On returns style with the row background applied, for a cell in a selected
// row. Passing false gives the style back unchanged, so a caller can write one
// expression for both cases.
func (t *Theme) On(style lipgloss.Style, selected bool) lipgloss.Style {
	if !selected {
		return style
	}
	return style.Background(colSelected)
}

// Six hues, each with one job. Amber is the interface accent — focus and
// selection — and is never a status, so a highlighted row cannot be mistaken
// for a warning.
//
// These are 256-colour values rather than the base sixteen because the base
// sixteen are whatever the terminal's theme says they are, and a status colour
// that means "healthy" in one profile and "washed-out olive" in another is not
// carrying information. lipgloss degrades them on a 16-colour terminal, where
// the glyph and the word still say everything the colour did.
const (
	colGreen   = lipgloss.Color("114")
	colAmber   = lipgloss.Color("214")
	colRed     = lipgloss.Color("203")
	colGrey    = lipgloss.Color("244")
	colFaint   = lipgloss.Color("240")
	colMagenta = lipgloss.Color("176")
	colCyan    = lipgloss.Color("75")

	// colSelected is the row highlight. Dark enough that white text stays
	// readable on it and light enough to find at a glance, which is the
	// whole job — the eye should land on the current row without hunting
	// for a marker.
	colSelected = lipgloss.Color("236")

	// colInverse is text printed on an accent-coloured block: the screen
	// name in the status bar.
	colInverse = lipgloss.Color("232")
)

// NewTheme builds the theme for the current terminal.
func NewTheme(ascii bool) *Theme {
	return &Theme{
		ASCII:    ascii,
		Title:    lipgloss.NewStyle().Bold(true),
		Header:   lipgloss.NewStyle().Foreground(colFaint),
		Dim:      lipgloss.NewStyle().Foreground(colGrey),
		Accent:   lipgloss.NewStyle().Foreground(colAmber),
		Selected: lipgloss.NewStyle().Foreground(colAmber).Bold(true),
		Bar:      lipgloss.NewStyle().Foreground(colCyan),
		Err:      lipgloss.NewStyle().Foreground(colRed),
		Chat:     lipgloss.NewStyle().Foreground(colMagenta),
		Rule:     lipgloss.NewStyle().Foreground(colFaint),
		Badge:    lipgloss.NewStyle().Foreground(colInverse).Background(colAmber).Bold(true),
		Spark:    lipgloss.NewStyle().Foreground(colCyan),
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
