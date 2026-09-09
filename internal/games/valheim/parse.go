package valheim

import (
	"strconv"
	"strings"
	"time"

	"github.com/camden-brown/garrison/internal/model"
)

// MetricSaveMillis is the fourth dashboard tile: how long the last world save
// took, in milliseconds.
const MetricSaveMillis = "world_save_ms"

// Parse turns one line of server output into a typed event.
//
// Hot path — called for every line of every running server — so it works by
// prefix and index rather than regexp, and returns a zero Event for anything
// it has nothing to say about.
//
// Every pattern below is matched against a captured session in
// testdata/session.log rather than against documentation. Two of them are not
// what a reasonable person would have guessed, and the guesses would have
// passed a test written from the same guess.
func (Game) Parse(line string) model.Event {
	body, at, ok := strip(line)
	if !ok {
		return model.Event{}
	}

	switch {
	// A player arrives in two stages. The socket opens first, carrying only
	// a Steam ID; the character name does not arrive until ZDOID, which in a
	// captured session was 57 seconds later while the player sat on the
	// character-select screen. Reporting a join here would put a nameless
	// player in the roster for a minute.
	case strings.HasPrefix(body, "Got connection SteamID "):
		return model.Event{
			Kind:    model.KindConnect,
			At:      at,
			SteamID: after(body, "Got connection SteamID "),
			Text:    "connecting",
			Raw:     line,
		}

	// The join proper. "Got character ZDOID from <name> : <id>:<n>" is also
	// what Valheim logs when a player dies, with a zero id — so the same
	// pattern carries two events and only the id tells them apart. A parser
	// that treated every ZDOID as a join would count each death as a
	// re-join and quietly inflate the session history.
	case strings.HasPrefix(body, "Got character ZDOID from "):
		return characterEvent(body, at, line)

	// Leaving is keyed by Steam ID and never mentions the name, so the
	// roster has to hold the id-to-name mapping from the ZDOID line to
	// resolve who left. That is why player state is a reducer and not a
	// per-line lookup.
	case strings.HasPrefix(body, "Closing socket "):
		return model.Event{
			Kind:    model.KindLeave,
			At:      at,
			SteamID: after(body, "Closing socket "),
			Text:    "disconnected",
			Raw:     line,
		}

	// The save metric. Valheim writes the world in five phases and only the
	// last carries the total; the earlier four are per-phase timings that
	// would each look like a complete save.
	case strings.HasPrefix(body, "World save (5/5) done. Total time ["):
		return saveEvent(body, at, line)

	case strings.HasPrefix(body, "ZNet.LoadWorld: "):
		return model.Event{Kind: model.KindInfo, At: at, Text: body, Raw: line}

	case body == "Game server connected":
		return model.Event{Kind: model.KindInfo, At: at, Text: "server ready", Raw: line}

	case body == "Shutting down", body == "ZNet Shutdown":
		return model.Event{Kind: model.KindInfo, At: at, Text: body, Raw: line}
	}

	return model.Event{}
}

// characterEvent handles the dual-purpose ZDOID line.
func characterEvent(body string, at time.Time, raw string) model.Event {
	rest := after(body, "Got character ZDOID from ")

	name, id, found := strings.Cut(rest, " : ")
	if !found {
		return model.Event{}
	}
	name = strings.TrimSpace(name)

	// A zero ZDO id is a death, not an arrival: the character's object has
	// been destroyed. Anything else is the character spawning in.
	if zeroZDO(id) {
		return model.Event{Kind: model.KindDeath, At: at, Player: name, Text: name + " died", Raw: raw}
	}
	return model.Event{Kind: model.KindJoin, At: at, Player: name, Text: name + " joined", Raw: raw}
}

// zeroZDO reports whether a "<id>:<n>" pair names the null object.
func zeroZDO(id string) bool {
	head, _, _ := strings.Cut(strings.TrimSpace(id), ":")
	return head == "0"
}

// saveEvent pulls the duration out of "... Total time [314ms]".
func saveEvent(body string, at time.Time, raw string) model.Event {
	open := strings.LastIndex(body, "[")
	close := strings.LastIndex(body, "]")
	if open < 0 || close < open {
		return model.Event{}
	}

	ms, err := strconv.ParseFloat(strings.TrimSuffix(body[open+1:close], "ms"), 64)
	if err != nil {
		return model.Event{}
	}
	return model.Event{
		Kind:   model.KindSave,
		At:     at,
		Text:   "world saved in " + body[open+1:close],
		Metric: MetricSaveMillis,
		Value:  ms,
		Raw:    raw,
	}
}

// strip removes the wrappers around a Valheim log line and returns the message
// with its timestamp.
//
// Lines arrive looking like:
//
//	Sep  9 19:58:17 supervisord: valheim-server 09/09/2026 19:58:17: Got character ZDOID …
//
// Two timestamps: the outer one is supervisord's, added by the container
// image, and the inner one is Valheim's own. A different image wraps
// differently or not at all, so the outer prefix is optional and the inner
// timestamp is what gets parsed — it is the one the game wrote.
func strip(line string) (body string, at time.Time, ok bool) {
	body = strings.TrimSpace(line)

	// Drop the image's supervisord prefix if it is there. Everything up to
	// and including the stream name goes; what is left starts with Valheim's
	// own timestamp.
	if i := strings.Index(body, "supervisord: valheim-server "); i >= 0 {
		body = body[i+len("supervisord: valheim-server "):]
	}

	// "09/09/2026 19:58:17: rest"
	const stamp = "01/02/2006 15:04:05"
	if len(body) > len(stamp)+2 && body[len(stamp)] == ':' {
		if t, err := time.Parse(stamp, body[:len(stamp)]); err == nil {
			return strings.TrimSpace(body[len(stamp)+1:]), t, true
		}
	}

	// No inner timestamp: image chatter, Unity warnings, SteamCMD output.
	// There is nothing here Garrison can classify.
	return "", time.Time{}, false
}

func after(s, prefix string) string { return strings.TrimSpace(strings.TrimPrefix(s, prefix)) }
