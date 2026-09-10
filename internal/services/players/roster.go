package players

import (
	"context"
	"time"

	"github.com/camden-brown/garrison/internal/model"
)

// The roster poller: for games that can be asked, ask.
//
// Valheim's roster is reconstructed from log lines, which is why debt #1
// exists — its log never links a name to a connection, so two players who
// finish loading out of order are mis-paired. Project Zomboid can simply be
// asked, and its answer is authoritative.
//
// Both end up in the same snapshot field, which is the point. The Players
// view, the fleet's occupancy column and the session tracker all read
// srv.Players and none of them knows which way it was populated.

// DefaultRosterInterval is how often a server that can be asked is asked.
//
// Ten seconds. The roster is what the fleet's player count and the session
// tracker's arrival times come from, so it wants to be fresher than the
// backup poll and slower than the stats stream — and every poll is a round
// trip to a single-threaded game server that would rather be simulating
// zombies.
const DefaultRosterInterval = 10 * time.Second

// Asker is a game's roster capability, already bound to a transport.
//
// Pre-bound for the same reason the drain is: this package must not import
// internal/games, so cmd asserts games.Rostered and adapts.
type Asker interface {
	// Ask returns who is connected. The second return says whether this
	// server can be asked at all — a game with no roster capability, or one
	// whose container is not running — which is a different answer from a
	// server that failed to reply.
	Ask(ctx context.Context, server string) (players []model.Player, ok bool, err error)
}

// RosterObserver receives what a server said about who is on it.
type RosterObserver interface {
	RosterObserved(ctx context.Context, at time.Time, server string, players []model.Player)
}

// Servers is the set to poll, which is the fleet as the store currently knows
// it.
type Servers interface {
	Names() []string
}

// RosterPoller asks each server that can be asked.
type RosterPoller struct {
	Interval time.Duration
	Servers  Servers
	Asker    Asker
}

// Run polls until the context is cancelled.
func (p *RosterPoller) Run(ctx context.Context, obs RosterObserver) {
	if p.Servers == nil || p.Asker == nil || obs == nil {
		return
	}
	interval := p.Interval
	if interval <= 0 {
		interval = DefaultRosterInterval
	}

	p.once(ctx, obs)

	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			p.once(ctx, obs)
		}
	}
}

func (p *RosterPoller) once(ctx context.Context, obs RosterObserver) {
	for _, server := range p.Servers.Names() {
		players, ok, err := p.Asker.Ask(ctx, server)
		if !ok {
			// This server cannot be asked: no capability, or not running.
			// Its roster stays whatever the log stream made of it, which
			// for a game like Valheim is the only answer there is.
			continue
		}
		if err != nil {
			// A server that can be asked and did not answer keeps its last
			// known roster rather than being emptied. An RCON timeout
			// during a save is not everybody leaving, and reporting it as
			// one would end every session in the history.
			continue
		}
		obs.RosterObserved(ctx, time.Now(), server, players)
	}
}
