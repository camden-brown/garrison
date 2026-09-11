package valheim

import (
	"strconv"
	"strings"

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
	KeyCrossplay  = "Crossplay"

	// World modifiers. The key is the WorldModifiers enum member, because it
	// is also the first argument to -modifier and having one spelling for
	// both means there is nothing to keep in sync.
	KeyCombat       = "Combat"
	KeyDeathPenalty = "DeathPenalty"
	KeyResources    = "Resources"
	KeyRaids        = "Raids"
	KeyPortals      = "Portals"

	// -setkey toggles, named for their GlobalKeys members.
	KeyNoBuildCost  = "NoBuildCost"
	KeyPlayerEvents = "PlayerEvents"
	KeyPassiveMobs  = "PassiveMobs"
	KeyNoMap        = "NoMap"
)

// modifier is one -modifier category and the options offered for it.
//
// Valheim parses both arguments with a case-insensitive Enum.TryParse against
// one shared WorldModifierOption enum, so the parser will accept any option
// for any category. It is the game's own settings screen that restricts which
// options mean anything per category, and applying a combination it does not
// know is not an error: ServerOptionsGUI.SetPreset finds no matching slider,
// logs "Missing settings for preset", and leaves the world alone.
//
// That failure mode is why options are a closed list here rather than free
// text. A value outside it produces a server that looks configured and is not.
type modifier struct {
	key     string
	label   string
	help    string
	options []games.Option
}

// The graded options, in the order the game's own slider presents them.
// testdata/world-modifiers.txt holds the enum they are drawn from.
var (
	optStandard = games.Option{Value: "", Label: "Standard"}

	modifiers = []modifier{
		{
			key:   KeyCombat,
			label: "Combat",
			help:  "How hard enemies hit and how hard they are to kill.",
			options: []games.Option{
				optStandard,
				{Value: "veryeasy", Label: "Very easy"},
				{Value: "easy", Label: "Easy"},
				{Value: "hard", Label: "Hard"},
				{Value: "veryhard", Label: "Very hard"},
			},
		},
		{
			key:   KeyDeathPenalty,
			label: "Death penalty",
			help: "Casual keeps your items and skills on death. Hardcore takes " +
				"both and leaves no tombstone.",
			options: []games.Option{
				optStandard,
				{Value: "casual", Label: "Casual", Note: "keep items and skills"},
				{Value: "veryeasy", Label: "Very easy"},
				{Value: "easy", Label: "Easy"},
				{Value: "hard", Label: "Hard"},
				{Value: "hardcore", Label: "Hardcore", Note: "no tombstone"},
			},
		},
		{
			key:   KeyResources,
			label: "Resource drops",
			help:  "Scales how much everything drops when gathered or killed.",
			options: []games.Option{
				optStandard,
				{Value: "muchless", Label: "Much less"},
				{Value: "less", Label: "Less"},
				{Value: "more", Label: "More"},
				{Value: "muchmore", Label: "Much more"},
				{Value: "most", Label: "Most"},
			},
		},
		{
			key:   KeyRaids,
			label: "Raids",
			help:  "How often the world sends an event at your base. None turns them off.",
			options: []games.Option{
				optStandard,
				{Value: "none", Label: "None"},
				{Value: "muchless", Label: "Much less"},
				{Value: "less", Label: "Less"},
				{Value: "more", Label: "More"},
				{Value: "muchmore", Label: "Much more"},
			},
		},
		{
			key:   KeyPortals,
			label: "Portals",
			help: "Casual lets metal through portals. Very hard removes portals " +
				"from the world entirely.",
			options: []games.Option{
				optStandard,
				{Value: "casual", Label: "Casual", Note: "metal through portals"},
				{Value: "hard", Label: "Hard"},
				{Value: "veryhard", Label: "Very hard", Note: "no portals"},
			},
		},
	}
)

// toggle is one -setkey flag.
//
// GlobalKeys holds far more than these four, but the rest divide into world
// progress (everything past the NonServerOption sentinel) and the rate keys,
// which take a value rather than being present or absent and which the game
// classifies separately as cheats. Both are jobs of their own; these four are
// the booleans the world settings screen actually offers.
type toggle struct {
	key       string
	globalKey string
	label     string
	help      string
}

var toggles = []toggle{
	{KeyNoBuildCost, "nobuildcost", "No build cost", "Building costs no materials."},
	{KeyPlayerEvents, "playerevents", "Player-triggered events", "Raids follow players rather than bases."},
	{KeyPassiveMobs, "passivemobs", "Passive creatures", "Nothing attacks unless attacked first."},
	{KeyNoMap, "nomap", "No map", "Removes the map and the minimap for everyone."},
}

// Schema is what the settings form renders.
//
// Every field here is ImpactRecreate. That is not laziness: Valheim has no
// config file and no live reload, so every one of these is an environment
// variable or a launch argument, and changing either means a new container.
// The form will say so on each of them, which is the honest thing and exactly
// the case ADR 0006 wanted surfaced early.
func (Game) Schema() games.Schema {
	fields := []games.Field{
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
			Help: "At least five characters, or empty for no password — which " +
				"Valheim allows only while \"List publicly\" is off. Edit it in " +
				"the server's TOML file; the form will not type a password " +
				"into one.",
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
		{
			Key:     KeyCrossplay,
			Label:   "Crossplay",
			Group:   "Access",
			Type:    games.TypeBool,
			Default: false,
			Help: "Joins the PlayFab network so console and Game Pass players can " +
				"connect. Steam players reach the server either way.",
			Impact: games.ImpactRecreate,
		},
	}

	for _, m := range modifiers {
		fields = append(fields, games.Field{
			Key:     m.key,
			Label:   m.label,
			Group:   "World",
			Type:    games.TypeEnum,
			Default: "",
			Options: m.options,
			Help:    m.help,
			Impact:  games.ImpactRecreate,
		})
	}

	for _, t := range toggles {
		fields = append(fields, games.Field{
			Key:     t.key,
			Label:   t.label,
			Group:   "Rules",
			Type:    games.TypeBool,
			Default: false,
			Help:    t.help,
			Impact:  games.ImpactRecreate,
		})
	}

	return games.Schema{Fields: fields}
}

// serverArgs builds the launch arguments the image appends verbatim.
//
// The image expands $SERVER_ARGS unquoted into the server's command line
// (valheim-server:124), so anything with a space in it splits into extra
// arguments. Nothing here is interpolated from free text: a modifier value is
// emitted only when it matches one of the options the schema declared, which
// makes the argument list a closed set no matter what somebody hand-edits into
// the TOML. Dropping an unrecognised value is deliberate — the alternative is
// passing it through to a server that would log it and ignore it anyway.
func serverArgs(inst model.Instance) string {
	var args []string

	for _, m := range modifiers {
		v := stringOr(inst, m.key, "")
		if v == "" || !m.allows(v) {
			continue
		}
		args = append(args, "-modifier", m.key, v)
	}

	for _, t := range toggles {
		if boolOf(inst, t.key, false) {
			args = append(args, "-setkey", t.globalKey)
		}
	}

	return strings.Join(args, " ")
}

// allows reports whether the option is one this modifier declared.
func (m modifier) allows(value string) bool {
	for _, o := range m.options {
		if s, ok := o.Value.(string); ok && s != "" && s == value {
			return true
		}
	}
	return false
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

func boolOf(inst model.Instance, key string, fallback bool) bool {
	if b, ok := inst.Settings[key].(bool); ok {
		return b
	}
	return fallback
}

func boolString(inst model.Instance, key string, fallback bool) string {
	if boolOf(inst, key, fallback) {
		return "1"
	}
	return "0"
}

// trueFalse is for the image's own switches, which it compares against the
// words rather than the digits SERVER_PUBLIC uses.
func trueFalse(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func itoa(n int) string { return strconv.Itoa(n) }
