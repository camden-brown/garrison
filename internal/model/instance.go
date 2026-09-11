package model

import "time"

// Instance is one configured game server: everything Garrison knows about it
// that does not come from Docker. It is the in-memory form of a file in
// %APPDATA%\Garrison\servers\<name>.toml.
type Instance struct {
	Name  string // "zomboid-main" — unique, and the container suffix
	Game  string // a games.Meta.ID
	Image string // pinned image reference
	// Data is a host path bind-mounted as the data volume, and Volume is a
	// runtime-managed volume used instead. Exactly one is set.
	//
	// Volume exists because on Windows the two are not equivalent: a bind
	// mount from a Windows drive is ~32x slower than a volume for many
	// small files, which is what a chunked world save is. See model.Mount.
	Data   string
	Volume string

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

// NamePrefix is what Garrison puts in front of the things it creates in the
// runtime, so a container or a volume is recognisably ours in somebody else's
// `docker ps`.
const NamePrefix = "garrison-"

// CacheVolume names a volume for data this instance can rebuild — a download
// the runtime would otherwise fetch again on every recreate.
//
// Named after the instance so two servers of the same game never share one:
// that would be two downloaders writing the same tree. The purpose is part of
// the name because a game may want more than one.
func (i Instance) CacheVolume(purpose string) string {
	return NamePrefix + i.Name + "-" + purpose
}

// Schedule is a recurring task attached to an instance.
type Schedule struct {
	Kind   string        // a tasks.Kind
	Cron   string        // standard five-field cron
	Drain  time.Duration // player warning window before a restart
	Policy string        // "skip-if-occupied" | "wait-for-empty" | "always"
}

// Storage is the mount an instance's world lives on, whichever form it takes.
//
// Plugins build their Plan from this rather than from Data directly, so a
// server switched to a volume needs no per-game change. A volume beats a path
// when both are somehow set, because it is the more deliberate choice — a
// path can be left over from before the switch.
func (i Instance) Storage(container string) Mount {
	if i.Volume != "" {
		return Mount{Volume: i.Volume, Container: container}
	}
	return Mount{Host: i.Data, Container: container}
}

// HasStorage reports whether this instance has anywhere to keep a world.
func (i Instance) HasStorage() bool { return i.Volume != "" || i.Data != "" }
