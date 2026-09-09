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

	// MetricLabels names the fourth dashboard tile per game id. It rides
	// along with the observation so the store never has to import the game
	// registry to find out what a plugin calls its metric.
	MetricLabels map[string]string
}

func (m FleetObserved) apply(s Snapshot) Snapshot {
	// Busy, the histories, the console tail and the roster are all
	// Garrison's own knowledge rather than the engine's, so they survive an
	// observation that would otherwise overwrite them. A poll lands every
	// five seconds; without this it would discard four seconds of samples
	// out of every five, which looks like a rendering bug for a week.
	prior := make(map[string]Server, len(s.Servers))
	for _, srv := range s.Servers {
		prior[srv.Name] = srv
	}

	observed := make([]Server, 0, len(m.Containers))
	for _, c := range m.Containers {
		was := prior[c.Instance]

		requested := was.StopRequested
		if c.State.Live() {
			// It is up again, so whatever we asked for last time is spent.
			requested = false
		}

		state, detail := classify(c, requested, was.StopGrace)

		srv := was
		srv.Name = c.Instance
		srv.Game = c.Game
		srv.ID = c.ID
		srv.State = state
		srv.Detail = detail
		srv.ExitCode = c.ExitCode
		srv.Started = c.Started
		srv.Restarts = c.Restarts
		srv.Ports = c.Ports
		srv.Health = c.Health
		srv.PlanHash = c.PlanHash
		srv.StopRequested = requested
		srv.Created = true
		if label, ok := m.MetricLabels[c.Game]; ok {
			srv.MetricLabel = label
		}
		observed = append(observed, srv)
	}

	s.At = m.At
	s.Engine.OK = true
	s.Engine.Err = ""
	s.Engine.LastOK = m.At
	return s.withServers(merge(s, observed))
}

// observedFrom keeps only the servers a container was seen for, discarding the
// configured-but-not-created rows so merge can rebuild them.
func observedFrom(servers []Server) []Server {
	out := make([]Server, 0, len(servers))
	for _, srv := range servers {
		if srv.Created {
			out = append(out, srv)
		}
	}
	return out
}

// merge is the union of what the engine reported and what the config
// directory says.
//
// The two sets differ in both directions and both differences are worth
// showing. A configured server with no container has not been created yet,
// which is not the same as stopped. A container with no file is one Garrison
// found by label — a fleet recovered after losing %APPDATA%, or something
// created by hand — and hiding it would be worse than showing it without its
// settings.
func merge(s Snapshot, observed []Server) []Server {
	out := make([]Server, 0, len(observed)+len(s.instances))
	seen := make(map[string]bool, len(observed))

	for _, srv := range observed {
		if inst, ok := s.instances[srv.Name]; ok {
			srv.Instance, srv.Configured = inst, true
			if srv.Game == "" {
				srv.Game = inst.Game
			}
		} else {
			srv.Instance, srv.Configured = model.Instance{}, false
		}
		seen[srv.Name] = true
		out = append(out, srv)
	}

	for name, inst := range s.instances {
		if seen[name] {
			continue
		}
		out = append(out, Server{
			Name:       name,
			Game:       inst.Game,
			Instance:   inst,
			Configured: true,
			State:      model.StateStopped,
			Detail:     "not created yet",
			Health:     model.Health{OK: true},
		})
	}
	return out
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

// maxConsole bounds the per-server log tail carried in the snapshot.
const maxConsole = 200

// LogEventsRead is a batch of parsed lines from one container.
//
// A batch rather than a line because a crash-looping mod produces thousands a
// second, and a mutation per line would put the store and the renderer under
// exactly the load the flood is warning about.
type LogEventsRead struct {
	At     time.Time
	Server string
	Events []model.Event
}

func (m LogEventsRead) apply(s Snapshot) Snapshot {
	if len(m.Events) == 0 {
		return s
	}

	s.At = m.At
	return s.withServers(mapServer(s.Servers, m.Server, func(srv *Server) {
		for _, ev := range m.Events {
			srv.Console = appendConsole(srv.Console, ev)
			rosterApply(srv, ev)

			if ev.Metric != "" {
				at := ev.At
				if at.IsZero() {
					at = m.At
				}
				srv.GameMetric, _, _ = srv.GameMetric.Add(at, ev.Value)
			}
		}
	}))
}

// rosterApply folds one event into the player list.
//
// The awkward part is that some games report identity in pieces. Valheim logs
// a Steam id when the socket opens, the character name up to a minute later
// with no id attached, and only the id again on departure — so nothing in the
// log directly links a name to the connection it belongs to.
//
// The binding here is therefore a heuristic: a newly named character claims
// the oldest connection still waiting for a name. It is right whenever joins
// do not overlap, which is nearly always, and it can mis-pair two players who
// finish loading in a different order than they connected. The alternative is
// no roster at all for games without RCON, and a game that can answer
// properly implements games.Rostered and never reaches this code.
func rosterApply(srv *Server, ev model.Event) {
	switch ev.Kind {
	case model.KindConnect:
		if ev.SteamID != "" {
			srv.connecting = append(append([]string(nil), srv.connecting...), ev.SteamID)
		}

	case model.KindJoin:
		if ev.Player == "" {
			return
		}
		steamID := ev.SteamID
		if steamID == "" && len(srv.connecting) > 0 {
			steamID = srv.connecting[0]
			srv.connecting = append([]string(nil), srv.connecting[1:]...)
		}
		srv.Players = withPlayer(srv.Players, model.Player{
			Name:    ev.Player,
			SteamID: steamID,
			Since:   ev.At,
		})

	case model.KindLeave:
		srv.Players = withoutPlayer(srv.Players, ev.SteamID, ev.Player)
		srv.connecting = withoutID(srv.connecting, ev.SteamID)
	}
}

// withPlayer adds or replaces a player, copying rather than writing through —
// the slice may be shared with a snapshot already being rendered.
func withPlayer(players []model.Player, p model.Player) []model.Player {
	out := make([]model.Player, 0, len(players)+1)
	replaced := false
	for _, existing := range players {
		if existing.Name == p.Name {
			out = append(out, p)
			replaced = true
			continue
		}
		out = append(out, existing)
	}
	if !replaced {
		out = append(out, p)
	}
	return out
}

// withoutPlayer removes by whichever identity the departure carried. Valheim's
// only names the Steam id.
func withoutPlayer(players []model.Player, steamID, name string) []model.Player {
	out := make([]model.Player, 0, len(players))
	for _, p := range players {
		if steamID != "" && p.SteamID == steamID {
			continue
		}
		if name != "" && p.Name == name {
			continue
		}
		out = append(out, p)
	}
	return out
}

func withoutID(ids []string, id string) []string {
	if id == "" {
		return ids
	}
	out := make([]string, 0, len(ids))
	for _, existing := range ids {
		if existing != id {
			out = append(out, existing)
		}
	}
	return out
}

// appendConsole adds a line to the bounded tail, always allocating so a
// snapshot already published keeps the slice it was given.
func appendConsole(console []model.Event, ev model.Event) []model.Event {
	if len(console) < maxConsole {
		out := make([]model.Event, len(console)+1)
		copy(out, console)
		out[len(console)] = ev
		return out
	}
	out := make([]model.Event, maxConsole)
	copy(out, console[len(console)-maxConsole+1:])
	out[maxConsole-1] = ev
	return out
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

// InstancesLoaded is what the config directory says.
//
// It replaces the whole set rather than merging, because the directory is the
// authority: a file somebody deleted should take its server with it.
type InstancesLoaded struct {
	At        time.Time
	Instances []model.Instance
}

func (m InstancesLoaded) apply(s Snapshot) Snapshot {
	byName := make(map[string]model.Instance, len(m.Instances))
	for _, inst := range m.Instances {
		byName[inst.Name] = inst
	}

	s.At = m.At
	s.instances = byName
	return s.withServers(merge(s, observedFrom(s.Servers)))
}

// Instance returns a server's configuration.
func (s Snapshot) Instance(name string) (model.Instance, bool) {
	inst, ok := s.instances[name]
	return inst, ok
}

// HostDescribed records the engine's own figures.
type HostDescribed struct {
	At   time.Time
	Info host.Info
}

func (m HostDescribed) apply(s Snapshot) Snapshot {
	s.At = m.At
	s.Engine.Version = m.Info.Version
	s.Engine.OS = m.Info.OS
	s.Engine.NCPU = m.Info.NCPU
	s.Engine.MemTotal = m.Info.MemTotal
	return s
}

// EngineResolved records which endpoint Garrison is talking to. It is sent
// once at startup so the status bar can show "unix · unreachable" instead of
// nothing at all before the first poll returns.
type EngineResolved struct {
	At        time.Time
	Endpoint  string
	Transport string
	Poll      time.Duration
}

func (m EngineResolved) apply(s Snapshot) Snapshot {
	s.At = m.At
	s.Engine.Endpoint = m.Endpoint
	s.Engine.Transport = m.Transport
	s.Engine.Poll = m.Poll
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
