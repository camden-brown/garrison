package valheim

import (
	"strconv"

	"github.com/camden-brown/garrison/internal/games"
	"github.com/camden-brown/garrison/internal/model"
)

// Setting keys. They are Valheim's own names where it has them, so what the
// form shows and what the container gets are recognisably the same thing.
const (
	KeyServerName = "ServerName"
	KeyWorldName  = "WorldName"
	KeyPassword   = "ServerPass"
	KeyPublic     = "ServerPublic"
)

// Schema is what the settings form renders.
//
// Every field here is ImpactRecreate. That is not laziness: Valheim has no
// config file and no live reload, so every one of these is an environment
// variable, and changing an environment variable means a new container. The
// form will say so on each of them, which is the honest thing and exactly the
// case ADR 0006 wanted surfaced early.
func (Game) Schema() games.Schema {
	return games.Schema{Fields: []games.Field{
		{
			Key:     KeyServerName,
			Label:   "Server name",
			Group:   "Identity",
			Type:    games.TypeString,
			Default: "",
			Help:    "Shown in the server browser. Defaults to the instance name.",
			Impact:  games.ImpactRecreate,
		},
		{
			Key:     KeyWorldName,
			Label:   "World name",
			Group:   "Identity",
			Type:    games.TypeString,
			Default: "",
			Help: "Names the save files on disk. Changing it starts a new world " +
				"rather than renaming the old one.",
			Impact: games.ImpactWipeRisk,
		},
		{
			Key:     KeyPassword,
			Label:   "Password",
			Group:   "Access",
			Type:    games.TypeSecret,
			Default: "",
			Help: "At least five characters, and Valheim rejects one that appears " +
				"in the server or world name.",
			Impact: games.ImpactRecreate,
		},
		{
			Key:     KeyPublic,
			Label:   "List publicly",
			Group:   "Access",
			Type:    games.TypeBool,
			Default: false,
			Help:    "Off keeps the server out of the browser; players join by IP.",
			Impact:  games.ImpactRecreate,
		},
	}}
}

// stringOr reads a string setting, falling back when it is absent or empty.
//
// Settings arrive from TOML as map[string]any, so every read is a type
// assertion that can fail. Failing softly to the default is right here: a
// malformed value should not stop a server from starting, and the settings
// form validates against Schema before anything is written.
func stringOr(inst model.Instance, key, fallback string) string {
	if v, ok := inst.Settings[key].(string); ok && v != "" {
		return v
	}
	return fallback
}

func boolString(inst model.Instance, key string, fallback bool) string {
	v := fallback
	if b, ok := inst.Settings[key].(bool); ok {
		v = b
	}
	if v {
		return "1"
	}
	return "0"
}

func itoa(n int) string { return strconv.Itoa(n) }
