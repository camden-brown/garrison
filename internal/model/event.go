package model

import "time"

// Kind classifies a parsed log line.
type Kind uint8

const (
	KindUnknown Kind = iota
	KindInfo
	KindWarn
	KindError
	KindJoin
	KindLeave
	KindChat
	KindDeath
	KindSave
	KindAdmin
	KindMetric
)

var kindNames = [...]string{
	"unknown", "info", "warn", "error", "join", "leave",
	"chat", "death", "save", "admin", "metric",
}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "unknown"
}

// Event is one classified log line. A game's Parse turns a raw line into one
// of these; the fan-out goroutine in internal/services/logs distributes it to
// the console buffer, the player table, the metric rings, the alert matcher
// and the SQLite event log.
type Event struct {
	Kind    Kind
	At      time.Time // parsed from the line when it carries a timestamp
	Player  string
	SteamID string
	Text    string // the human-meaningful remainder
	Metric  string // e.g. "zombies_alive"; with Value, feeds the fourth tile
	Value   float64
	Raw     string // the original line, for the console view
}

// Drop reports whether Parse declined to classify the line. Games return a
// zero Event for output they have nothing to say about.
func (e Event) Drop() bool { return e.Kind == KindUnknown && e.Raw == "" }
