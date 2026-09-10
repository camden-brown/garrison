package zomboid

import (
	"strings"
	"time"

	"github.com/camden-brown/garrison/internal/model"
)

// Parsing Project Zomboid's stdout.
//
// The shape of a line is:
//
//	[26-08-26 19:14:26.045] WARN : Script  f:0 st:519,349,439 at Method> message.
//
// A day-month-year date with a two-digit year, a severity, a channel, a frame
// counter, three clock numbers, an optional method, and then the message after
// a ">".
//
// **What is not here is the interesting part.** Joins, leaves, chat and deaths
// do not reach stdout: the server writes them to user.txt, chat.txt and
// connections.txt inside the data volume, and a container's log stream never
// sees them. Verified against a real server's logs — a session with players
// coming and going produced zero join lines on stdout.
//
// That is why this game implements games.Rostered and Valheim does not, and it
// is the cleanest vindication of the capability split so far: the same Players
// view works over a roster reconstructed from a log and one asked for over
// RCON, and neither the view nor the store knows which it is looking at.

// logStamp is the game's own timestamp layout. Day first, two-digit year, and
// no zone at all — which is why Plan pins the container to UTC.
const logStamp = "02-01-06 15:04:05.000"

// Parse classifies one line of server output.
func (Game) Parse(line string) model.Event {
	body, severity, at, ok := strip(line)
	if !ok {
		return model.Event{}
	}

	ev := model.Event{At: at, Raw: line, Text: body}

	switch severity {
	case "ERROR":
		ev.Kind = model.KindError
	case "WARN":
		ev.Kind = model.KindWarn
	default:
		ev.Kind = model.KindInfo
	}

	// The few lines on stdout that mean something an operator would act on.
	// Everything else keeps the severity it arrived with.
	switch {
	case strings.Contains(body, "SaveAll") || strings.HasPrefix(body, "Saving"):
		ev.Kind = model.KindSave
	case strings.Contains(body, "server is now listening") ||
		strings.Contains(body, "RCON: listening"):
		ev.Kind = model.KindInfo
	case strings.Contains(body, "admin closed server"):
		ev.Kind = model.KindAdmin
	}

	return ev
}

// strip peels the prefix off a line, returning the message, the severity and
// the timestamp.
//
// A line without the prefix is the JVM's own output, SteamCMD's, or the
// image's shell script — real output worth showing in the console, but not
// something this function can say anything about. It comes back as unclassified
// info with the raw line intact rather than being dropped, because a stack
// trace is exactly the thing you want to still be able to read.
func strip(line string) (body, severity string, at time.Time, ok bool) {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "[") {
		return "", "", time.Time{}, false
	}

	close := strings.Index(s, "]")
	if close < 0 {
		return "", "", time.Time{}, false
	}

	stamp := s[1:close]
	parsed, err := time.ParseInLocation(logStamp, stamp, time.UTC)
	if err != nil {
		return "", "", time.Time{}, false
	}

	rest := strings.TrimSpace(s[close+1:])

	// Severity up to the colon that separates it from the channel.
	colon := strings.Index(rest, ":")
	if colon < 0 {
		// A timestamped line with no severity: the chat and connection logs
		// look like this, and so does the image's own output.
		return trimTrailingDot(rest), "", parsed, true
	}
	severity = strings.TrimSpace(rest[:colon])

	// The message is everything after the first ">", which closes the
	// bookkeeping the game puts between the channel and what it wanted to
	// say. No ">" means the line is all bookkeeping.
	if arrow := strings.Index(rest, ">"); arrow >= 0 {
		return trimTrailingDot(strings.TrimSpace(rest[arrow+1:])), severity, parsed, true
	}
	return trimTrailingDot(strings.TrimSpace(rest[colon+1:])), severity, parsed, true
}

// trimTrailingDot drops the full stop the game puts on the end of almost every
// line, which reads as a typo once the lines are in a table.
func trimTrailingDot(s string) string {
	return strings.TrimRight(s, ".")
}
