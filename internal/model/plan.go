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
