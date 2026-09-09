package model

import "time"

// Player is one connected player, however the game reports them: over RCON, an
// HTTP API, or reconstructed from log events for games that offer neither.
type Player struct {
	Name    string
	SteamID string
	Since   time.Time
	PingMS  int
	Extra   map[string]string // game-specific: coordinates, character, deaths
}
