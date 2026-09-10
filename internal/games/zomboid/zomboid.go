// Package zomboid supports Project Zomboid dedicated servers.
//
// It is the second game, and the one M3 exists to test: two config files in
// two syntaxes, RCON, and Workshop mods whose order matters. Valheim shaped
// the interface by having none of those; Zomboid is where it finds out whether
// the shape was right.
//
// Three things about this game drive most of what is here.
//
// It writes 144 keys into servertest.ini and 269 into a Lua table, and both
// files are written whole. A key Garrison does not know about is a key
// Garrison would delete, so the plugin carries every one of them — see
// keys_gen.go and ADR 0011.
//
// Its player events do not reach stdout. Joins, leaves and chat go to separate
// files inside the data volume, so nothing in Parse can see them; the roster
// comes over RCON instead, which is exactly the capability games.Rostered was
// designed for and the first implementation of it.
//
// And the container image rewrites thirteen config keys from environment
// variables on every boot, so Plan and Compile cannot disagree about them.
package zomboid

import (
	"time"

	"github.com/camden-brown/garrison/internal/games"
	"github.com/camden-brown/garrison/internal/model"
)

// Game is the Project Zomboid plugin. Stateless: Meta and Schema are called on
// every render and Parse on every log line.
type Game struct{}

func init() { games.Register(Game{}) }

var _ games.Game = Game{}

// Ports. The game port and the UDP port are consecutive by convention and the
// server writes both into its config; RCON is TCP and separate.
const (
	DefaultPort = 16261
	DefaultUDP  = 16262
	DefaultRCON = 27015
)

func (Game) Meta() games.Meta {
	return games.Meta{
		ID:         "zomboid",
		Name:       "Project Zomboid",
		SteamAppID: "380870",
		// The community image, and the one whose startup script the env
		// mapping in Plan was read out of.
		DefaultImage: "renegademaster/zomboid-dedicated-server:latest",
		DefaultPorts: []model.PortMap{
			{Container: "16261/udp", Host: DefaultPort},
			{Container: "16262/udp", Host: DefaultUDP},
			{Container: "27015/tcp", Host: DefaultRCON},
		},
		// No fourth tile. DESIGN wants "zombies alive" here, and the server
		// offers no way to ask: it is not on stdout and there is no RCON
		// command for it. An empty label means the dashboard shows network
		// I/O instead, which is the honest fallback — a tile that invented
		// a number would be worse than one that admits it has none.
		MetricLabel: "",
	}
}

// Plan is the container Project Zomboid wants.
//
// The environment here is not an alternative to Compile — it is a mirror of
// it, and has to be. The image's run_server.sh calls apply_postinstall_config
// on every boot, which rewrites thirteen keys in servertest.ini from these
// variables: SaveWorldEveryMinutes, DefaultPort, UDPPort, MaxPlayers, Mods,
// Map, WorkshopItems, PauseEmpty, Open, RCONPassword, RCONPort, PublicName and
// Password. Whatever Compile writes to those keys, the image overwrites from
// here.
//
// So both sides are derived from the same settings. Getting that wrong does
// not fail loudly: it produces a server whose config file says one thing, whose
// running state says another, and whose settings screen agrees with neither.
func (Game) Plan(inst model.Instance) (model.Plan, error) {
	image := inst.Image
	if image == "" {
		image = Game{}.Meta().DefaultImage
	}

	ports := inst.Ports
	if len(ports) == 0 {
		ports = Game{}.Meta().DefaultPorts
	}

	env := map[string]string{
		// The thirteen the image reconciles, from the same settings Compile
		// reads. Anything added to that list in the image has to be added
		// here too, or the file and the container drift apart.
		"SERVER_NAME":       serverName(inst),
		"PUBLIC_SERVER":     boolSetting(inst, "Open"),
		"SERVER_PASSWORD":   stringSetting(inst, "Password"),
		"MAX_PLAYERS":       intSetting(inst, "MaxPlayers"),
		"PAUSE_ON_EMPTY":    boolSetting(inst, "PauseEmpty"),
		"AUTOSAVE_INTERVAL": intSetting(inst, "SaveWorldEveryMinutes"),
		"MAP_NAMES":         stringSetting(inst, "Map"),
		"MOD_NAMES":         stringSetting(inst, "Mods"),
		"MOD_WORKSHOP_IDS":  workshopIDs(inst),
		"DEFAULT_PORT":      portFor(ports, "16261/udp", DefaultPort),
		"UDP_PORT":          portFor(ports, "16262/udp", DefaultUDP),
		"RCON_PORT":         portFor(ports, "27015/tcp", DefaultRCON),
		"RCON_PASSWORD":     rconPassword(inst),

		// Admin credentials are the image's own, not the game's config.
		"ADMIN_USERNAME": stringOr(inst, KeyAdminUser, "admin"),
		"ADMIN_PASSWORD": stringOr(inst, KeyAdminPass, ""),

		// The JVM heap. Derived from the container's memory limit rather
		// than set separately, because two numbers that must agree and can
		// be edited apart will eventually disagree — and the way that
		// surfaces is a server the kernel kills mid-save.
		"MAX_RAM": heapFor(inst.Resources.Memory),
	}

	return model.Plan{
		Image:  image,
		Env:    env,
		Ports:  ports,
		Mounts: []model.Mount{{Host: inst.Data, Container: "/home/steam/Zomboid"}},
		Resources: model.Resources{
			Memory: inst.Resources.Memory,
			CPUs:   inst.Resources.CPUs,
		},
		// The image traps SIGTERM and issues an RCON quit, which is what
		// flushes the world. Two minutes because a populated Knox County
		// takes far longer to write than a Valheim seed, and a save killed
		// halfway is the failure this grace exists to avoid.
		StopSignal: "SIGTERM",
		StopGrace:  120 * time.Second,
	}, nil
}
