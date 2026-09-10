package zomboid

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/camden-brown/garrison/internal/games"
	"github.com/camden-brown/garrison/internal/model"
)

// Settings keys that are Garrison's rather than the game's.
//
// Both are the image's, not Project Zomboid's: the admin account is created by
// the startup script and never appears in servertest.ini, so it has nowhere to
// live in the generated tables.
const (
	KeyAdminUser = "AdminUsername"
	KeyAdminPass = "AdminPassword"
)

// sandboxPrefix namespaces the Lua settings away from the ini ones.
//
// The two files share key names — Map is an ini key and MapName is not, but
// there is enough overlap in a 413-key surface to make collisions a matter of
// when rather than whether. A prefix is cheaper than discovering the clash
// through a setting that writes itself into the wrong file.
const sandboxPrefix = "Sandbox."

// promoted are the settings that appear in the form without pressing "a".
//
// Everything else is Advanced, which is not a judgement about importance but
// about how a form of 413 fields is usable at all. These are the ones an
// operator changes in the first week: the shape of the game, not the depth of
// its loot tables.
var promoted = map[string]string{
	// ini key -> group
	"PublicName":            "Identity",
	"PublicDescription":     "Identity",
	"Open":                  "Access",
	"Password":              "Access",
	"MaxPlayers":            "Access",
	"PVP":                   "Gameplay",
	"PauseEmpty":            "Gameplay",
	"SaveWorldEveryMinutes": "Gameplay",
	"Map":                   "World",
	"Mods":                  "Mods",
	"WorkshopItems":         "Mods",
	"RCONPort":              "Network",
	"RCONPassword":          "Network",
	"DefaultPort":           "Network",
	"UDPPort":               "Network",

	// sandbox key -> group, without the prefix
	"Zombies":            "World",
	"Distribution":       "World",
	"ZombieRespawn":      "World",
	"DayLength":          "World",
	"StartMonth":         "World",
	"XpMultiplier":       "Gameplay",
	"LootRespawn":        "Gameplay",
	"AllClothesUnlocked": "Gameplay",
}

// wipeRisk are the settings that can cost a world.
//
// Map changes which world the server opens and ResetID forces every client to
// re-download it, which in practice means starting again. Both get the typed
// confirmation, which is the third of DESIGN's three friction points doing its
// job for a game that has more than one way to lose a save.
var wipeRisk = map[string]bool{
	"Map":     true,
	"ResetID": true,
}

// recreate are the settings that are fixed when the container is built, so
// changing them needs a new one rather than a restart.
var recreate = map[string]bool{
	"DefaultPort":   true,
	"UDPPort":       true,
	"RCONPort":      true,
	"RCONPassword":  true,
	"Mods":          true,
	"WorkshopItems": true,
	"Password":      true,
	"Open":          true,
	"MaxPlayers":    true,
	"PauseEmpty":    true,
	"PublicName":    true,
	KeyAdminUser:    true,
	KeyAdminPass:    true,
}

// Schema is the whole settings surface: 144 ini keys, 269 sandbox variables,
// and the two the image adds.
//
// It is built from the generated tables rather than written out, so the form
// and Compile cannot disagree about what exists. A curated handful is
// promoted; the rest is Advanced, which is what makes a form this size
// navigable — the group rail plus "a" is the difference between a settings
// screen and a config file with borders.
func (Game) Schema() games.Schema {
	fields := make([]games.Field, 0, len(iniSettings)+len(sandboxSettings)+2)

	fields = append(fields,
		games.Field{
			Key: KeyAdminUser, Label: "Admin username", Group: "Access",
			Type: games.TypeString, Default: "admin",
			Help:   "The account the server creates for administration.",
			Impact: games.ImpactRecreate,
		},
		games.Field{
			Key: KeyAdminPass, Label: "Admin password", Group: "Access",
			Type: games.TypeSecret, Default: "",
			Help:   "Set it in the server's TOML — the form will not type a password into one.",
			Impact: games.ImpactRecreate,
		},
	)

	for _, s := range iniSettings {
		fields = append(fields, fieldFor(s, s.Key, "Server"))
	}
	for _, s := range sandboxSettings {
		fields = append(fields, fieldFor(s, sandboxPrefix+s.Key, "Sandbox"))
	}
	return games.Schema{Fields: fields}
}

// fieldFor turns one generated setting into a form field.
func fieldFor(s setting, key, fallbackGroup string) games.Field {
	group, isPromoted := promoted[s.Key]
	if !isPromoted {
		group = fallbackGroup
	}

	f := games.Field{
		Key:      key,
		Label:    label(s.Key),
		Group:    group,
		Default:  s.Default,
		Advanced: !isPromoted,
		Impact:   games.ImpactRestart,
	}

	switch {
	case wipeRisk[s.Key]:
		f.Impact = games.ImpactWipeRisk
	case recreate[s.Key]:
		f.Impact = games.ImpactRecreate
	}

	switch {
	case len(s.Options) > 0:
		// The game numbers these itself and documents the numbering in the
		// config it writes, so the form offers exactly those and nothing
		// invented — see testdata/sandboxvars.keys.
		f.Type = games.TypeEnum
		f.Options = make([]games.Option, 0, len(s.Options))
		for _, o := range s.Options {
			f.Options = append(f.Options, games.Option{Value: o.Value, Label: o.Label})
		}
	case s.Type == "bool":
		f.Type = games.TypeBool
	case s.Type == "int":
		f.Type = games.TypeInt
	case s.Type == "float":
		f.Type = games.TypeFloat
	case isSecret(s.Key):
		f.Type = games.TypeSecret
	default:
		f.Type = games.TypeString
	}
	return f
}

// isSecret is the one place a key's name decides how it is treated, and it is
// about not rendering something rather than about what it means.
func isSecret(key string) bool {
	lower := strings.ToLower(key)
	return strings.Contains(lower, "password") || strings.Contains(lower, "token")
}

// label turns a config key into something a person reads: "SaveWorldEveryMinutes"
// becomes "Save world every minutes", which is imperfect English and still
// better than the key.
func label(key string) string {
	var b strings.Builder
	for i, r := range key {
		if i > 0 && r >= 'A' && r <= 'Z' {
			// Do not split an acronym: RCONPort is not "R C O N Port".
			prev := rune(key[i-1])
			if !(prev >= 'A' && prev <= 'Z') {
				b.WriteByte(' ')
				b.WriteRune(r + 32)
				continue
			}
		}
		b.WriteRune(r)
	}
	out := b.String()
	if out == "" {
		return key
	}
	return strings.ToUpper(out[:1]) + out[1:]
}

// ---- reading settings ----------------------------------------------------
//
// Settings arrive from TOML as map[string]any, so every read is an assertion
// that can fail. Failing to the game's own default is right: a malformed value
// should not stop a server from starting, and the form validates against
// Schema before anything is written.

func lookup(inst model.Instance, key string) (any, bool) {
	v, ok := inst.Settings[key]
	return v, ok && v != nil
}

func stringSetting(inst model.Instance, key string) string {
	if v, ok := lookup(inst, key); ok {
		return fmt.Sprint(v)
	}
	if d, ok := defaultFor(key); ok {
		return fmt.Sprint(d)
	}
	return ""
}

func stringOr(inst model.Instance, key, fallback string) string {
	if v, ok := lookup(inst, key); ok {
		if s := fmt.Sprint(v); s != "" {
			return s
		}
	}
	return fallback
}

func boolSetting(inst model.Instance, key string) string {
	if v, ok := lookup(inst, key); ok {
		if b, ok := v.(bool); ok {
			return strconv.FormatBool(b)
		}
		if s, ok := v.(string); ok && (s == "true" || s == "false") {
			return s
		}
	}
	if d, ok := defaultFor(key); ok {
		return fmt.Sprint(d)
	}
	return "false"
}

func intSetting(inst model.Instance, key string) string {
	if v, ok := lookup(inst, key); ok {
		switch n := v.(type) {
		case int:
			return strconv.Itoa(n)
		case int64:
			return strconv.FormatInt(n, 10)
		case float64:
			return strconv.Itoa(int(n))
		case string:
			if _, err := strconv.Atoi(n); err == nil {
				return n
			}
		}
	}
	if d, ok := defaultFor(key); ok {
		return fmt.Sprint(d)
	}
	return "0"
}

// defaults indexes the generated tables so a read can fall back to what the
// game itself would have used.
var defaults = func() map[string]any {
	out := make(map[string]any, len(iniSettings)+len(sandboxSettings))
	for _, s := range iniSettings {
		out[s.Key] = s.Default
	}
	for _, s := range sandboxSettings {
		out[sandboxPrefix+s.Key] = s.Default
	}
	return out
}()

func defaultFor(key string) (any, bool) {
	v, ok := defaults[key]
	return v, ok
}

func serverName(inst model.Instance) string {
	if s := stringOr(inst, "PublicName", ""); s != "" {
		return s
	}
	return inst.Name
}

// rconPassword refuses to fall back to the image's "changeme_rcon".
//
// An RCON port open to a default password is worse than one that does not
// work, because the failure is silent and remote. Empty means the plugin's
// Commandable will say it has no password rather than trying one everybody
// knows.
func rconPassword(inst model.Instance) string {
	return stringOr(inst, "RCONPassword", "")
}

// workshopIDs is the Workshop half of the mod list, in load order.
//
// The order is the slice order in the TOML, which is what LoadOrderMatters
// promises: Project Zomboid loads mods in the order WorkshopItems lists them
// and a dependency after its dependent is a server that will not start.
func workshopIDs(inst model.Instance) string {
	ids := make([]string, 0, len(inst.Mods))
	for _, m := range inst.Mods {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		return stringSetting(inst, "WorkshopItems")
	}
	return strings.Join(ids, ";")
}

func portFor(ports []model.PortMap, container string, fallback int) string {
	for _, p := range ports {
		if p.Container == container {
			return strconv.Itoa(p.Host)
		}
	}
	return strconv.Itoa(fallback)
}

// heapFor is the JVM heap the image should give the server, derived from the
// container's memory limit rather than configured separately.
//
// Three quarters, floored at 2 GiB: the JVM needs headroom above the heap for
// metaspace, thread stacks and the off-heap buffers the network layer uses,
// and a heap set to the whole limit is a container the kernel kills instead of
// a server that garbage-collects.
func heapFor(limit int64) string {
	const mib = 1 << 20
	heap := limit / mib * 3 / 4
	if limit == 0 || heap < 2048 {
		heap = 2048
	}
	return strconv.FormatInt(heap, 10) + "m"
}
