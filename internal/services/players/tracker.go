// Package players turns a live roster into session history.
//
// The roster itself is reconstructed elsewhere — in the store, from the log
// events a game's Parse produced, or from a roster endpoint for games that
// have one. This package watches that roster change and writes down the
// arrivals and departures, because "who is on now" is a fact the fleet poll
// already carries and "who was on last Tuesday" is not.
//
// It diffs a roster rather than consuming join and leave events directly, so
// it works the same for a game that reports its roster over RCON as for one
// whose roster is inferred from a log. A game that grows games.Rostered
// changes where the roster comes from and nothing here.
package players

import (
	"context"
	"time"

	"github.com/camden-brown/garrison/internal/model"
)

// DefaultInterval is how often the roster is diffed.
//
// A second would be wasted: the roster is itself derived from a log stream
// batched at 100ms, and a session's start time is not interesting to that
// precision. Five seconds bounds how wrong an interrupted session's end can
// be, which is the only thing the interval really decides.
const DefaultInterval = 5 * time.Second

// DefaultWindow is how much history the view is given. DESIGN asks for seven
// days of sessions and occupancy by hour, and both come out of the same list.
const DefaultWindow = 7 * 24 * time.Hour

// Fleet is where the current rosters come from, keyed by server.
//
// Declared here rather than taken as a store, because a service never imports
// core: cmd knows both and wires them together (ADR 0007).
type Fleet interface {
	Rosters() map[string][]model.Player
}

// Recorder is the durable half. It is internal/store in practice.
type Recorder interface {
	SessionOpened(server string, p model.Player, at time.Time) (int64, error)
	SessionClosed(id int64, at time.Time) error
	SessionsSince(server string, since time.Time) ([]model.Session, error)
}

// Observer receives the history worth rendering.
type Observer interface {
	SessionsListed(ctx context.Context, at time.Time, server string, sessions []model.Session)
}

// Tracker follows rosters and records sessions.
type Tracker struct {
	Interval time.Duration
	Window   time.Duration
	Fleet    Fleet
	Store    Recorder

	// open maps a server and player to the session row that is still open,
	// so a departure can close the row its arrival created.
	open map[string]map[string]int64
}

// key identifies a player across polls.
//
// The Steam id where there is one, because a player can change their display
// name mid-session and a name-keyed roster would record that as one player
// leaving and another arriving. The name is the fallback for games that report
// no id at all.
func key(p model.Player) string {
	if p.SteamID != "" {
		return p.SteamID
	}
	return p.Name
}

// Run diffs the roster until the context is cancelled.
func (t *Tracker) Run(ctx context.Context, obs Observer) {
	if t.Fleet == nil || t.Store == nil || obs == nil {
		return
	}
	interval := t.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}

	t.once(ctx, obs)

	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			// Sessions still open are left open. Closing them here would
			// date them to the moment Garrison exited, which is right only
			// if the server exited too — and CloseStaleSessions on the next
			// start is where that judgement belongs.
			return
		case <-tick.C:
			t.once(ctx, obs)
		}
	}
}

func (t *Tracker) once(ctx context.Context, obs Observer) {
	if t.open == nil {
		t.open = map[string]map[string]int64{}
	}
	window := t.Window
	if window <= 0 {
		window = DefaultWindow
	}
	now := time.Now()

	for server, roster := range t.Fleet.Rosters() {
		if t.open[server] == nil {
			t.open[server] = map[string]int64{}
		}
		here := t.open[server]

		// Arrivals: on the roster, no open row.
		seen := make(map[string]bool, len(roster))
		for _, p := range roster {
			k := key(p)
			seen[k] = true
			if _, ok := here[k]; ok {
				continue
			}
			// A session's start is when the player joined if the roster
			// says, because a restart of Garrison should not restart
			// everyone's session clock.
			at := p.Since
			if at.IsZero() {
				at = now
			}
			id, err := t.Store.SessionOpened(server, p, at)
			if err != nil {
				continue
			}
			here[k] = id
		}

		// Departures: an open row, no longer on the roster.
		for k, id := range here {
			if seen[k] {
				continue
			}
			if err := t.Store.SessionClosed(id, now); err != nil {
				continue
			}
			delete(here, k)
		}

		sessions, err := t.Store.SessionsSince(server, now.Add(-window))
		if err != nil {
			continue
		}
		obs.SessionsListed(ctx, now, server, sessions)
	}
}
