package core

import (
	"fmt"
	"time"

	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/model"
)

// Mutation is one fact about the world, applied by the single writer.
//
// Mutations are named as past-tense facts — FleetObserved, OperationEnded —
// and never as commands. A name that describes what happened cannot be
// accidentally applied twice with a different meaning, and it forces the
// question "what did I actually learn?" at the point where state changes.
//
// apply is unexported, so mutations can only be defined here. That is what
// keeps "one writer" true: a producer in another package sends one of these,
// it does not invent one.
type Mutation interface {
	apply(Snapshot) Snapshot
}

// FleetObserved is a complete poll of the container engine. It is the whole
// fleet rather than a delta because the engine is the authority: a container
// that vanished between polls should vanish from the list, and reconciling
// deltas against a source of truth we can just re-read would be work in
// exchange for a class of bug.
type FleetObserved struct {
	At         time.Time
	Containers []host.Container
}

func (m FleetObserved) apply(s Snapshot) Snapshot {
	// Busy and StopRequested are Garrison's own knowledge, not the engine's,
	// so they survive the observation that would otherwise overwrite them.
	// Histories, like Busy, are Garrison's own knowledge rather than the
	// engine's. A poll every five seconds must not throw away four seconds
	// of samples.
	prior := make(map[string]Server, len(s.Servers))
	busy := make(map[string]Op, len(s.Servers))
	stopped := make(map[string]bool, len(s.Servers))
	grace := make(map[string]time.Duration, len(s.Servers))
	for _, srv := range s.Servers {
		prior[srv.Name] = srv
		if srv.Busy != OpNone {
			busy[srv.Name] = srv.Busy
		}
		if srv.StopRequested {
			stopped[srv.Name] = true
		}
		if srv.StopGrace > 0 {
			grace[srv.Name] = srv.StopGrace
		}
	}

	servers := make([]Server, 0, len(m.Containers))
	for _, c := range m.Containers {
		requested := stopped[c.Instance]
		if c.State.Live() {
			// It is up again, so whatever we asked for last time is spent.
			requested = false
		}

		state, detail := classify(c, requested, grace[c.Instance])
		was := prior[c.Instance]
		servers = append(servers, Server{
			CPU:           was.CPU,
			Mem:           was.Mem,
			MemLimit:      was.MemLimit,
			Name:          c.Instance,
			Game:          c.Game,
			ID:            c.ID,
			State:         state,
			Detail:        detail,
			ExitCode:      c.ExitCode,
			Started:       c.Started,
			Restarts:      c.Restarts,
			Ports:         c.Ports,
			Health:        c.Health,
			PlanHash:      c.PlanHash,
			Busy:          busy[c.Instance],
			StopGrace:     grace[c.Instance],
			StopRequested: requested,
		})
	}

	s.At = m.At
	s.Engine.OK = true
	s.Engine.Err = ""
	s.Engine.LastOK = m.At
	return s.withServers(servers)
}

// classify turns what the engine reported into what the operator should read,
// using the one thing the engine cannot know: whether Garrison asked for this.
//
// A server that ignores SIGTERM is killed when its grace period runs out and
// exits 137 — identical to a crash, and identical to an OOM kill. Reporting a
// shutdown we requested as a crash is the fleet view lying, and it buries the
// fact that actually matters: the server did not stop in time, so its save may
// not have finished writing. That is a Zomboid server needing longer than its
// configured grace, and it is the sort of thing you want to read once rather
// than diagnose twice.
//
// An OOM kill stays a crash even during a requested stop. The kernel stepping
// in is news regardless of what we were doing at the time.
func classify(c host.Container, stopRequested bool, grace time.Duration) (model.State, string) {
	detail := c.Detail
	if detail == "" && c.State == model.StateStopped {
		detail = "exit 0"
	}

	if !stopRequested || c.OOMKilled || c.State != model.StateCrashed {
		return c.State, detail
	}

	switch {
	case c.ExitCode == 137 && grace > 0:
		return model.StateStopped, "killed after " + Budget(grace) + " grace"
	case c.ExitCode == 137:
		// No grace recorded, so this stop was requested by an earlier
		// process. Say what happened without inventing a number.
		return model.StateStopped, "killed — did not stop in time"
	case c.ExitCode > 128:
		return model.StateStopped, fmt.Sprintf("stopped on signal %d", c.ExitCode-128)
	default:
		return model.StateStopped, fmt.Sprintf("stopped, exit %d", c.ExitCode)
	}
}

// FleetUnobservable is the engine failing to answer.
//
// It does not empty the fleet. Every server goes to StateUnknown — the "?"
// glyph, magenta — because "I cannot see it" is a different fact from "it is
// off", and a dashboard that quietly redraws a running fleet as stopped is
// worse than one that admits it lost contact.
type FleetUnobservable struct {
	At  time.Time
	Err error
}

func (m FleetUnobservable) apply(s Snapshot) Snapshot {
	wasOK := s.Engine.OK

	servers := make([]Server, 0, len(s.Servers))
	for _, srv := range s.Servers {
		srv.State = model.StateUnknown
		srv.Detail = "engine unreachable"
		servers = append(servers, srv)
	}

	s.At = m.At
	s.Engine.OK = false
	if m.Err != nil {
		s.Engine.Err = m.Err.Error()
	}
	s = s.withServers(servers)

	// Only on the transition. The poller retries every few seconds and a
	// notice per attempt would bury everything else within a minute of
	// Docker Desktop restarting for an update.
	if wasOK {
		s = s.withNotice(Notice{At: m.At, Level: LevelError, Text: "docker unreachable: " + s.Engine.Err})
	}
	return s
}

// StatsSampled is one point from a container's stats stream.
//
// It carries the sample rather than the whole history because the history
// lives in the snapshot: appending is the reducer's job, and doing it here
// means only the one series being written is copied rather than the fleet.
type StatsSampled struct {
	At     time.Time
	Server string
	Sample host.Sample
}

func (m StatsSampled) apply(s Snapshot) Snapshot {
	sampledAt := m.Sample.At
	if sampledAt.IsZero() {
		sampledAt = m.At
	}

	s.At = m.At
	return s.withServers(mapServer(s.Servers, m.Server, func(srv *Server) {
		// The cold points are discarded for now. They are 30 days of
		// history bound for SQLite, which arrives with M2's persistence.
		srv.CPU, _, _ = srv.CPU.Add(sampledAt, m.Sample.CPUPct)
		srv.Mem, _, _ = srv.Mem.Add(sampledAt, float64(m.Sample.MemBytes))
		srv.MemLimit = m.Sample.MemLimit

		// Docker's network counters are cumulative since the container
		// booted, so the interesting number is the difference. The first
		// sample has nothing to subtract from and a restart resets the
		// counters, so both report zero rather than a spike the size of
		// everything the server has ever sent.
		total := m.Sample.NetRxBytes + m.Sample.NetTxBytes
		if srv.netTotal > 0 && total >= srv.netTotal {
			elapsed := sampledAt.Sub(lastAt(srv.Net)).Seconds()
			if elapsed > 0 {
				srv.Net, _, _ = srv.Net.Add(sampledAt, float64(total-srv.netTotal)/elapsed)
			}
		}
		srv.netTotal = total
	}))
}

// lastAt is when a history was last written, or the zero time if never.
func lastAt(h model.History) time.Time {
	if p, ok := h.Last(); ok {
		return p.At
	}
	return time.Time{}
}

// OperationBegan marks a server busy. The fleet view redraws on the keystroke
// rather than on the next poll, which is the difference between an interface
// that feels alive and one you press twice.
type OperationBegan struct {
	At     time.Time
	Server string
	Op     Op
	Grace  time.Duration // OpStop only: how long before it kills
}

func (m OperationBegan) apply(s Snapshot) Snapshot {
	s.At = m.At
	return s.withServers(mapServer(s.Servers, m.Server, func(srv *Server) {
		srv.Busy = m.Op
		if m.Op == OpStop {
			srv.StopGrace = m.Grace
		}
		if m.Op == OpStart {
			// Starting it again retires the last stop we asked for, so a
			// later crash is reported as one.
			srv.StopRequested = false
		}
	}))
}

// OperationEnded clears it, and says so if it failed.
//
// The state itself is not written here: what the container actually did is the
// engine's to report, and the next poll will say. Writing an optimistic
// "running" and being wrong is how a fleet view starts lying.
type OperationEnded struct {
	At     time.Time
	Server string
	Op     Op
	Err    error
}

func (m OperationEnded) apply(s Snapshot) Snapshot {
	s.At = m.At
	s = s.withServers(mapServer(s.Servers, m.Server, func(srv *Server) {
		srv.Busy = OpNone
		if m.Op == OpStop && m.Err == nil {
			srv.StopRequested = true
		}
	}))
	if m.Err != nil {
		s = s.withNotice(Notice{
			At:     m.At,
			Level:  LevelError,
			Server: m.Server,
			Text:   m.Err.Error(),
		})
	}
	return s
}

// EngineResolved records which endpoint Garrison is talking to. It is sent
// once at startup so the status bar can show "unix · unreachable" instead of
// nothing at all before the first poll returns.
type EngineResolved struct {
	At        time.Time
	Endpoint  string
	Transport string
}

func (m EngineResolved) apply(s Snapshot) Snapshot {
	s.At = m.At
	s.Engine.Endpoint = m.Endpoint
	s.Engine.Transport = m.Transport
	return s
}

// NoticeRaised is how a layer with nothing else to say reports a problem.
type NoticeRaised struct {
	At     time.Time
	Level  Level
	Server string
	Text   string
}

func (m NoticeRaised) apply(s Snapshot) Snapshot {
	s.At = m.At
	return s.withNotice(Notice{At: m.At, Level: m.Level, Server: m.Server, Text: m.Text})
}

// mapServer copies the server list, applying f to the one named. Copying
// rather than writing in place is what makes a published snapshot immutable —
// a reducer that mutated s.Servers[i] would change a snapshot the TUI is
// halfway through rendering.
func mapServer(servers []Server, name string, f func(*Server)) []Server {
	out := make([]Server, len(servers))
	copy(out, servers)
	for i := range out {
		if out[i].Name == name {
			f(&out[i])
		}
	}
	return out
}

// Reduce applies mutations to a snapshot and returns the result.
//
// It is exported because it is a pure function — snapshot in, snapshot out —
// and building a snapshot is how everything above the store gets tested: a
// view's golden render needs a fixed fleet, not a running writer goroutine and
// a wait for it to catch up.
//
// This does not weaken the single-writer rule. That rule is about who assigns
// to the store's snapshot field, and the answer is still the one goroutine in
// Run. Reduce touches no shared state and hands back a value.
func Reduce(s Snapshot, ms ...Mutation) Snapshot {
	for _, m := range ms {
		s = m.apply(s)
	}
	return s
}
