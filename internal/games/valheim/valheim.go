// Package valheim supports Valheim dedicated servers.
//
// It is the first game implemented, chosen because it is the least
// accommodating one that is still simple (see ADR 0006). Configuration is
// entirely environment variables, so Compile returns no files at all and the
// interface has to handle that case honestly from day one. There is no RCON,
// so everything Garrison knows about who is connected is reconstructed from
// log lines — which is the constraint that shapes Parse below.
package valheim

import (
	"time"

	"github.com/camden-brown/garrison/internal/games"
	"github.com/camden-brown/garrison/internal/model"
)

// Game is the Valheim plugin. It holds no state: every method is pure, because
// Meta and Schema are called on every render and Parse on every log line.
type Game struct{}

func init() { games.Register(Game{}) }

// Compile-time proof of the contract.
var _ games.Game = Game{}

func (Game) Meta() games.Meta {
	return games.Meta{
		ID:         "valheim",
		Name:       "Valheim",
		SteamAppID: "896660",
		// The image is the community standard and the one the log fixtures
		// were captured from. Its output carries a supervisord prefix that
		// Parse has to see past — see parse.go.
		DefaultImage: "lloesche/valheim-server",
		DefaultPorts: []model.PortMap{
			{Container: "2456/udp", Host: 2456},
			{Container: "2457/udp", Host: 2457},
		},
		// The fourth dashboard tile. Valheim writes the whole world on a
		// timer, and how long that takes is the number that tells you the
		// save is growing or the disk is struggling — a captured session
		// went from 59ms on an empty world to 314ms once one player had
		// modified a chunk.
		MetricLabel: "world save",
		MetricKey:   MetricSaveMillis,
	}
}

// Plan is the container Valheim wants.
//
// Everything is environment: that is what makes Valheim the honest first game,
// because it means a settings change needs a new container rather than a file
// rewrite, and the interface has to say so.
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
		"SERVER_NAME":   stringOr(inst, KeyServerName, inst.Name),
		"WORLD_NAME":    stringOr(inst, KeyWorldName, inst.Name),
		"SERVER_PUBLIC": boolString(inst, KeyPublic, false),
		"SERVER_PORT":   itoa(ports[0].Host),
		"CROSSPLAY":     boolString(inst, KeyCrossplay, false),
		// The container's clock is pinned to UTC because Parse has to read
		// the wall-clock time Valheim writes into its log, and that line
		// carries no offset. Left to the image this is UTC by default —
		// but a default is not a guarantee, and if it ever changed, every
		// timestamp in the console and the activity feed would silently
		// shift by the local offset while still looking plausible. Pinning
		// it makes the assumption something Garrison controls.
		"TZ": "UTC",
		// Garrison supervises this container. The image ships three of its
		// own schedules, and every one of them acts on the server behind
		// our back:
		//
		//   UPDATE_CRON  */15 * * * *  update, restarting the server
		//   RESTART_CRON 10 5 * * *    a daily bounce at 05:10
		//   BACKUPS      true, hourly  a copy into /config/backups
		//
		// The first two produce a stop nobody asked for, which the fleet
		// view can only report as a crash, and which would race whatever
		// task is holding the server's lane. The third writes into the
		// directory internal/services/backup archives, so every Garrison
		// backup would swallow the image's backups too and grow by the
		// hour. All three are Garrison's jobs; the schedule for them lives
		// in the server's TOML.
		"UPDATE_CRON":  "",
		"RESTART_CRON": "",
		"BACKUPS":      "false",
	}
	// BepInEx follows from whether any mods are configured, rather than
	// being a switch of its own. A loader with no plugins changes nothing
	// but the startup path, and a plugin with no loader is a file the
	// server never reads — so the two are one fact and a person cannot set
	// them to disagree. Removing the last mod puts the server back on the
	// vanilla binary, which is the same answer read the other way.
	//
	// It is set explicitly either way: the value is part of the plan hash,
	// so the container is recreated when this flips.
	env["BEPINEX"] = trueFalse(len(inst.Mods) > 0)

	// Always set, even to empty. The image reads
	// SERVER_PASS=${SERVER_PASS-secret} — the one-dash form, which fills in
	// only for a variable that is *unset* — so omitting it for a server with
	// no password would hand the server the image's own "secret" instead of
	// no password at all. Setting it empty passes -password "" through, which
	// is how the image's own disableServerPassword switch works.
	//
	// Valheim only accepts that on a server that is not publicly listed; a
	// public one with no password is refused by the game, not by this.
	env["SERVER_PASS"] = stringOr(inst, KeyPassword, "")
	// World modifiers and -setkey flags are launch arguments, not
	// environment, so they go through the one variable the image appends to
	// the command line. Empty means the world keeps whatever it has.
	if args := serverArgs(inst); args != "" {
		env["SERVER_ARGS"] = args
	}

	return model.Plan{
		Image:  image,
		Env:    env,
		Ports:  ports,
		Mounts: []model.Mount{inst.Storage("/config")},
		Resources: model.Resources{
			Memory: inst.Resources.Memory,
			CPUs:   inst.Resources.CPUs,
		},
		// SIGINT, not SIGTERM. The image traps it to shut the server down
		// cleanly, which is what writes the world; SIGTERM is the signal
		// that gets a save killed halfway through.
		StopSignal: "SIGINT",
		// A captured save took 314ms on a nearly empty world. A populated
		// one takes far longer, and the cost of guessing low is a corrupt
		// chunk file discovered weeks later.
		StopGrace: 120 * time.Second,
	}, nil
}

// Compile returns no files.
//
// This is not an oversight and the empty slice is the point: Valheim is
// configured entirely through the environment, so there is nothing to write to
// the data volume. ADR 0006 keeps Compile in the interface anyway, because
// Zomboid at M3 needs two files in two syntaxes and designing it away here
// would mean rebuilding it there.
func (Game) Compile(model.Instance) ([]model.File, error) { return nil, nil }
