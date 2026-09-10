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

// Mount is a bind mount from the host into the container.
type Mount struct {
	Host      string
	Container string
	ReadOnly  bool
}

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
