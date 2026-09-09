// Package core is Garrison's store: one writer, immutable snapshots.
//
// Every producer of state — the fleet poller today, stats and log streams and
// task steps from M1 on — sends a typed mutation on one channel. One goroutine
// applies them and publishes a Snapshot. Readers get a value they can hold for
// as long as they like, because nothing will ever change it underneath them.
//
// The TUI's Update does nothing but render a snapshot. That is what makes
// views pure functions, golden render tests possible, and a garbled frame at
// 2am something that cannot happen rather than something to hunt for.
//
// See docs/decisions/0004-single-writer-store.md.
package core

import (
	"sort"
	"time"

	"github.com/camden-brown/garrison/internal/model"
)

// Snapshot is the whole of what Garrison knows, at one instant.
//
// Every field is either a value or a slice the store never writes to again, so
// a snapshot is safe to hold, compare and render from another goroutine.
type Snapshot struct {
	Seq     uint64 // increments once per applied mutation; cheap change detection
	At      time.Time
	Engine  Engine
	Servers []Server
	Notices []Notice
}

// Engine is what Garrison knows about the container runtime it is talking to.
type Engine struct {
	Endpoint  string // "npipe:////./pipe/docker_engine"
	Transport string // "npipe", "unix" — what the status bar shows
	OK        bool
	Err       string    // why not, when OK is false
	LastOK    time.Time // when it last answered, so "down for 4m" is sayable
}

// Server is one managed container as the UI needs it.
//
// This is deliberately not host.Container. A view that imported internal/host
// to read a field would be reaching past the store, and the next thing it
// reaches for is a call. Everything a view needs is here or it is a missing
// field, which is a change to this struct and a line in a reducer.
type Server struct {
	Name     string // the instance name: "zomboid-main"
	Game     string
	ID       string
	State    model.State
	Detail   string // "exit 137", "OOM killed", "engine unreachable"
	ExitCode int
	Started  time.Time
	Restarts int
	Ports    []model.PortMap
	Health   model.Health
	PlanHash string

	// Busy is the operation running against this server, empty when none.
	// It is what lets the fleet view say "stopping…" the instant the key is
	// pressed rather than five seconds later when the poll catches up.
	Busy Op
}

// Uptime is how long the server has been up at the given instant, or zero if
// it is not running.
func (s Server) Uptime(now time.Time) time.Duration {
	if !s.State.Live() || s.Started.IsZero() {
		return 0
	}
	return now.Sub(s.Started)
}

// Op is something Garrison is doing to a server. At M2 these become task
// kinds with steps and compensation; for now they are the two verbs the fleet
// view offers.
type Op string

const (
	OpNone  Op = ""
	OpStart Op = "start"
	OpStop  Op = "stop"
)

// Present is the progressive form, which is what a busy row says.
func (o Op) Present() string {
	switch o {
	case OpStart:
		return "starting"
	case OpStop:
		return "stopping"
	}
	return ""
}

// Level classifies a notice.
type Level uint8

const (
	LevelInfo Level = iota
	LevelWarn
	LevelError
)

// Notice is something that happened and is worth saying: an operation that
// failed, the engine going away. The alert matcher in M2 grows out of this.
type Notice struct {
	At     time.Time
	Level  Level
	Server string // empty for fleet-wide notices
	Text   string
}

// maxNotices bounds the notice list. Every buffer in Garrison is bounded on
// purpose: this process is meant to stay open for weeks.
const maxNotices = 100

// Server looks one up by instance name.
func (s Snapshot) Server(name string) (Server, bool) {
	for _, srv := range s.Servers {
		if srv.Name == name {
			return srv, true
		}
	}
	return Server{}, false
}

// Counts is the fleet summary the status bar shows.
func (s Snapshot) Counts() (up, down, unknown int) {
	for _, srv := range s.Servers {
		switch {
		case srv.State == model.StateUnknown:
			unknown++
		case srv.State.Live():
			up++
		default:
			down++
		}
	}
	return up, down, unknown
}

// withServers returns a copy carrying a fresh, name-sorted server list. Sorted
// because the engine's order is not stable and a fleet list that reshuffles
// under the cursor is unusable.
func (s Snapshot) withServers(servers []Server) Snapshot {
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	s.Servers = servers
	return s
}

// withNotice appends a notice, dropping the oldest past the cap.
func (s Snapshot) withNotice(n Notice) Snapshot {
	next := make([]Notice, 0, len(s.Notices)+1)
	next = append(next, s.Notices...)
	next = append(next, n)
	if len(next) > maxNotices {
		next = next[len(next)-maxNotices:]
	}
	s.Notices = next
	return s
}
