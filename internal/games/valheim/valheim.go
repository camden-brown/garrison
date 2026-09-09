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
		// Garrison supervises restarts. The image's own updater restarting
		// the server behind our back would produce a stop we did not ask
		// for, which the fleet view would have to report as a crash.
		"UPDATE_CRON": "",
	}
	if pass := stringOr(inst, KeyPassword, ""); pass != "" {
		env["SERVER_PASS"] = pass
	}

	return model.Plan{
		Image:  image,
		Env:    env,
		Ports:  ports,
		Mounts: []model.Mount{{Host: inst.Data, Container: "/config"}},
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
