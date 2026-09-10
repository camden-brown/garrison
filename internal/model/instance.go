package model

import "time"

// Instance is one configured game server: everything Garrison knows about it
// that does not come from Docker. It is the in-memory form of a file in
// %APPDATA%\Garrison\servers\<name>.toml.
type Instance struct {
	Name  string // "zomboid-main" — unique, and the container suffix
	Game  string // a games.Meta.ID
	Image string // pinned image reference
	Data  string // host path bind-mounted as the data volume

	// Address is how players reach this server from outside: a hostname or
	// a public IP, without a port.
	//
	// Garrison cannot work this out. The published port is on the container
	// and the machine's LAN address is in the engine, but what a player types
	// is a DNS name somebody registered and a router somebody forwarded, and
	// neither of those is visible from here. Empty means "not told", and the
	// share text says so rather than guessing at a LAN address that will not
	// work for anyone outside the house.
	Address string

	Resources Resources
	Ports     []PortMap
	Settings  map[string]any // keys are the game's own; validated against its Schema
	Mods      []ModRef       // slice order is load order where the game cares
	Schedules []Schedule
}

// Resources caps what a server may consume. A zero value means unlimited,
// which is a bad idea for a game server and the wizard never proposes it.
type Resources struct {
	Memory int64   // bytes
	CPUs   float64 // fractional cores
}

// PortMap maps a container port to a host port.
type PortMap struct {
	Container string // "16261/udp" — protocol included
	Host      int
}

// ModRef is a mod as configured. Pin is empty to track latest.
type ModRef struct {
	ID  string
	Pin string
}

// Schedule is a recurring task attached to an instance.
type Schedule struct {
	Kind   string        // a tasks.Kind
	Cron   string        // standard five-field cron
	Drain  time.Duration // player warning window before a restart
	Policy string        // "skip-if-occupied" | "wait-for-empty" | "always"
}
