// Package host is the boundary between Garrison and whatever actually runs a
// container. Docker is one implementation; nothing above this package knows
// which one is in use, which is the seam that would let a remote or SSH driver
// drop in later.
//
// Named host rather than runtime so it does not shadow the standard library.
//
// This package must not import internal/games, internal/core or internal/tui:
// it consumes a model.Plan and knows nothing about who produced it.
package host

import (
	"context"
	"io"
	"time"

	"github.com/camden-brown/garrison/internal/model"
)

// Driver is the only interface Garrison uses to run containers. Everything
// above it is testable against a fake implementation with Docker stopped.
type Driver interface {
	// List returns every container Garrison manages, found by label rather
	// than by local bookkeeping — the fleet is rediscoverable after losing
	// %APPDATA%.
	List(ctx context.Context) ([]Container, error)
	Inspect(ctx context.Context, id string) (Container, error)

	Create(ctx context.Context, inst model.Instance, plan model.Plan) (id string, err error)
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string, signal string, grace time.Duration) error
	Remove(ctx context.Context, id string, withVolumes bool) error

	// Stats streams roughly one sample per second until ctx is cancelled.
	// The channel is closed when the stream ends.
	Stats(ctx context.Context, id string) (<-chan Sample, error)

	// Logs follows a container's combined output, demultiplexed, starting
	// from the last tail lines.
	Logs(ctx context.Context, id string, tail int) (io.ReadCloser, error)

	// Exec runs a command inside a container and returns its output.
	Exec(ctx context.Context, id string, argv []string) ([]byte, error)

	// Ping reports whether the engine is reachable. A failure is surfaced as
	// StateUnknown across the fleet rather than rendered as "stopped".
	Ping(ctx context.Context) error

	// Info describes the machine the containers are running on. It changes
	// about never, so callers poll it rarely.
	Info(ctx context.Context) (Info, error)
}

// Info is what the engine says about its host.
//
// The capacity figures are what turn a container's usage into a proportion:
// 27 GiB means nothing until you know the machine has 64, and a server at 200%
// CPU is either fine or catastrophic depending on how many cores there are.
type Info struct {
	Version    string // engine version, for the host panel
	OS         string // "Docker Desktop", "Ubuntu 22.04.5 LTS"
	NCPU       int
	MemTotal   int64
	Containers int
}

// Labels Garrison stamps on every container it creates. PlanHash lets startup
// reconciliation notice that a container no longer matches what its config
// would produce, and report the drift instead of silently correcting it.
const (
	LabelManaged  = "garrison.managed"
	LabelInstance = "garrison.instance"
	LabelGame     = "garrison.game"
	LabelPlanHash = "garrison.plan"
)

// NamePrefix is prepended to an instance name to make a container name, so
// `docker ps` is readable and Garrison's containers sort together.
const NamePrefix = "garrison-"

// ContainerName is the container Garrison creates for an instance.
func ContainerName(instance string) string { return NamePrefix + instance }

// Container is the observed state of one managed container.
type Container struct {
	ID       string
	Name     string
	Instance string // from LabelInstance
	Game     string // from LabelGame
	PlanHash string // from LabelPlanHash
	State    model.State
	Detail   string // why State is what it is: "OOM killed", "paused", "unhealthy: …"
	ExitCode int
	// OOMKilled is kept as its own fact rather than left inside Detail: a
	// server the kernel killed is a crash even when Garrison was the one
	// asking it to stop, and the layer that decides that should not have to
	// match on a human-readable string.
	OOMKilled bool
	Started   time.Time
	Restarts  int // consecutive restarts, for crash-loop detection
	Health    model.Health
	Ports     []model.PortMap
}

// Sample is one point of container resource usage.
//
// CPUPct is already computed from deltas. MemBytes excludes page cache: under
// the WSL2 backend the raw usage figure includes it, and reporting that makes
// every server look like it is about to be killed.
type Sample struct {
	At         time.Time
	CPUPct     float64
	MemBytes   int64
	MemLimit   int64
	NetRxBytes int64
	NetTxBytes int64
}
