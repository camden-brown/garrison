package model

import "time"

// Plan is the container a game wants for an instance.
//
// Games produce a Plan and never touch Docker; internal/host is the only thing
// that turns one into a real container. Plan is deliberately a plain value so
// it can be hashed, diffed against a running container, and printed in the
// wizard's review step before anything is created.
type Plan struct {
	Image      string
	Cmd        []string
	Env        map[string]string
	Ports      []PortMap
	Mounts     []Mount
	Labels     map[string]string
	Resources  Resources
	StopSignal string        // "SIGTERM", or "SIGINT" for games that trap it to save
	StopGrace  time.Duration // how long to wait before killing
	Health     *HealthCheck  // nil when the game offers no meaningful check

	// RCON says where this game's command channel is, and is the zero value
	// for a game that has none.
	//
	// The plugin declares it rather than the caller working it out, because
	// which container port carries RCON and which setting holds its password
	// are game facts. Without this, building a connection would mean a
	// switch on the game's id outside internal/games — the one smell that
	// matters most.
	RCON RCONSpec
}

// RCONSpec locates a game's command channel.
type RCONSpec struct {
	// ContainerPort is the port spec as it appears in Ports, e.g.
	// "27015/tcp". The host side is looked up from the running container,
	// since that is where a published port actually lands.
	ContainerPort string

	// Password is resolved by the plugin from the instance's settings. Empty
	// means the channel is not usable, which a plugin should prefer to a
	// default everybody knows.
	Password string
}

// HasRCON reports whether a plan describes a usable command channel.
func (p Plan) HasRCON() bool {
	return p.RCON.ContainerPort != "" && p.RCON.Password != ""
}

// Mount is storage attached to a container: either a path on the host or a
// volume the runtime manages.
//
// Both forms exist because on Windows they are not close to equivalent.
// Measured on Docker Desktop 29.7.2 (see CLAUDE.md, "Platform facts"), a bind
// mount from a Windows drive is about four times slower than a named volume
// for sequential writes and **about thirty times slower for many small
// files** — which is the shape of a chunked world save. A game that writes
// its save as hundreds of small files pays that every autosave.
//
// A bind mount is still the right default: the world is visible in Explorer,
// backed up by whatever already backs up that drive, and readable without
// Docker. A volume is the choice for a save-heavy game where that trade is
// worth making, and the cost is that the files live inside the runtime.
type Mount struct {
	// Host is a path on the host. Empty when Volume is set.
	Host string

	// Volume is a runtime-managed volume name. Empty when Host is set.
	//
	// Garrison never invents one: the wizard proposes a name and the
	// server's TOML records it, so a volume is as explicit as a path and a
	// world cannot end up somewhere nobody chose.
	Volume string

	Container string
	ReadOnly  bool

	// Cache marks a mount whose contents the server can rebuild from
	// somewhere else — a download the runtime would otherwise fetch again
	// every time the container is recreated.
	//
	// It is the one kind of volume a delete may remove. A world is the
	// thing Garrison exists to protect and is never removed with the
	// server; a two-gigabyte copy of the game files is not worth keeping
	// after the server that used them is gone.
	Cache bool
}

// Caches are the mounts a delete may take with it.
func Caches(mounts []Mount) []Mount {
	var out []Mount
	for _, m := range mounts {
		if m.Cache && m.IsVolume() {
			out = append(out, m)
		}
	}
	return out
}

// Source is what the runtime should attach, whichever form this mount takes.
func (m Mount) Source() string {
	if m.Volume != "" {
		return m.Volume
	}
	return m.Host
}

// IsVolume reports whether this mount is a runtime-managed volume rather than
// a host path.
func (m Mount) IsVolume() bool { return m.Volume != "" }

// HealthCheck is a container-level healthcheck.
type HealthCheck struct {
	Test     []string
	Interval time.Duration
	Timeout  time.Duration
	Retries  int
}

// Health is an observed health result, from the container check or a game's
// own probe.
type Health struct {
	OK        bool
	Detail    string // why not, when OK is false
	CheckedAt time.Time
}
