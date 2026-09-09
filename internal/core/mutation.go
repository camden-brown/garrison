package core

import (
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
	// Busy is Garrison's own knowledge, not the engine's, so it survives the
	// observation that would otherwise overwrite it.
	busy := make(map[string]Op, len(s.Servers))
	for _, srv := range s.Servers {
		if srv.Busy != OpNone {
			busy[srv.Name] = srv.Busy
		}
	}

	servers := make([]Server, 0, len(m.Containers))
	for _, c := range m.Containers {
		servers = append(servers, Server{
			Name:     c.Instance,
			Game:     c.Game,
			ID:       c.ID,
			State:    c.State,
			Detail:   detailFor(c),
			ExitCode: c.ExitCode,
			Started:  c.Started,
			Restarts: c.Restarts,
			Ports:    c.Ports,
			Health:   c.Health,
			PlanHash: c.PlanHash,
			Busy:     busy[c.Instance],
		})
	}

	s.At = m.At
	s.Engine.OK = true
	s.Engine.Err = ""
	s.Engine.LastOK = m.At
	return s.withServers(servers)
}

// detailFor is the short phrase under a state. The driver supplies one for the
// states where the reason is not obvious; a clean exit gets its code, because
// "stopped · exit 0" and "stopped" after a crash-and-restart read differently.
func detailFor(c host.Container) string {
	if c.Detail != "" {
		return c.Detail
	}
	if c.State == model.StateStopped {
		return "exit 0"
	}
	return ""
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

// OperationBegan marks a server busy. The fleet view redraws on the keystroke
// rather than on the next poll, which is the difference between an interface
// that feels alive and one you press twice.
type OperationBegan struct {
	At     time.Time
	Server string
	Op     Op
}

func (m OperationBegan) apply(s Snapshot) Snapshot {
	s.At = m.At
	return s.withServers(mapServer(s.Servers, m.Server, func(srv *Server) {
		srv.Busy = m.Op
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
