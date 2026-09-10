package comp

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/camden-brown/garrison/internal/model"
)

// LogLine is one console row: an event, and how many identical ones it stands
// for.
type LogLine struct {
	Event model.Event
	Count int
}

// Text is what the line says — the game's classification of it where it made
// one, the raw output otherwise.
func (l LogLine) Text() string {
	if l.Event.Text != "" {
		return l.Event.Text
	}
	return l.Event.Raw
}

// CollapseRepeats folds runs of identical lines into counters.
//
// A crash-looping mod otherwise erases the last hour of history in seconds:
// the buffer fills with one repeated line, and the thing that caused it
// scrolls off before anybody reads it. Collapsing keeps the cause on screen
// and turns the flood into a number, which is the more useful reading of it
// anyway.
//
// Events with nothing to say are dropped. A bare connection is a fact the
// roster needs and not a line worth a row.
func CollapseRepeats(events []model.Event) []LogLine {
	out := make([]LogLine, 0, len(events))
	for _, ev := range events {
		text := ev.Text
		if text == "" {
			text = ev.Raw
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Text() == text {
			out[n-1].Count++
			continue
		}
		out = append(out, LogLine{Event: ev, Count: 1})
	}
	return out
}

// KindGlyph is the one-cell marker for an event's classification.
//
// The glyph carries the meaning alongside the colour rather than instead of
// it: a 16-colour terminal, a colourblind reader and a copy-pasted screenshot
// all lose the colour and keep this.
func KindGlyph(t *Theme, k model.Kind) string {
	if t.ASCII {
		switch k {
		case model.KindJoin:
			return ">"
		case model.KindLeave:
			return "<"
		case model.KindDeath, model.KindError:
			return "x"
		case model.KindWarn:
			return "!"
		}
		return "."
	}
	switch k {
	case model.KindJoin:
		return "→"
	case model.KindLeave:
		return "←"
	case model.KindDeath, model.KindError:
		return "✕"
	case model.KindWarn:
		return "!"
	case model.KindChat:
		return "\""
	}
	return "·"
}

// KindStyle is the colour for an event's classification.
func KindStyle(t *Theme, k model.Kind) lipgloss.Style {
	switch k {
	case model.KindDeath, model.KindError:
		return t.Err
	case model.KindWarn:
		return t.Accent
	case model.KindChat:
		return t.Chat
	}
	return t.Dim
}
