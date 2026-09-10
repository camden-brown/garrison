package core

import (
	"fmt"
	"sort"
	"time"

	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tasks"
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

// stillPending drops draft entries the config has caught up with, which is
// what clears the badge after an apply without the view having to be told.
func stillPending(draft map[string]any, inst model.Instance) map[string]any {
	if len(draft) == 0 {
		return nil
	}
	out := make(map[string]any, len(draft))
	for k, v := range draft {
		if !sameValue(v, inst.Settings[k]) {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
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
			srv.Draft = stillPending(srv.Draft, inst)
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
			srv.Console = srv.Console.Add(ev)
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

// TaskProgressed is the engine reporting where a task got to.
//
// It replaces the entry with the same id rather than appending, because a task
// reports many times and a list of every step of every task is a log, not a
// view of what is happening.
type TaskProgressed struct {
	At       time.Time
	Progress tasks.Progress
}

func (m TaskProgressed) apply(s Snapshot) Snapshot {
	s.At = m.At

	next := make([]tasks.Progress, 0, len(s.Tasks)+1)
	replaced := false
	for _, t := range s.Tasks {
		if t.ID == m.Progress.ID {
			next = append(next, m.Progress)
			replaced = true
			continue
		}
		next = append(next, t)
	}
	if !replaced {
		next = append(next, m.Progress)
	}
	if len(next) > maxTasks {
		next = next[len(next)-maxTasks:]
	}
	s.Tasks = next

	// A delete that finished means the server is gone, and the snapshot is
	// where "gone" has to show. Reacting to the completed fact rather than
	// to the action that asked for it means a delete run as a subcommand
	// drops the row too, and a delete that rolled back does not.
	if m.Progress.Kind == tasks.KindDelete && m.Progress.State == tasks.StateDone {
		s = s.forget(m.Progress.Server)
	}

	// A task that failed is worth saying out loud. One that was cancelled
	// is not: the operator asked for it and already knows.
	if m.Progress.State == tasks.StateFailed || m.Progress.State == tasks.StateRolledBack {
		s = s.withNotice(Notice{
			At: m.At, Level: LevelError, Server: m.Progress.Server, Text: m.Progress.Err,
		})
	}

	// What a server is busy with is not separate knowledge from what the
	// engine is doing to it. Deriving one from the other means they cannot
	// disagree — which they did when a task failed in a way that skipped
	// whatever was supposed to clear the flag.
	return s.withServers(mapServer(s.Servers, m.Progress.Server, func(srv *Server) {
		srv.Busy = busyFrom(s, m.Progress.Server)

		// The stop grace is sticky: it is shown while stopping and again
		// afterwards, if the server had to be killed, to say how long it
		// was given.
		if grace, ok := stopGraceFrom(m.Progress); ok {
			srv.StopGrace = grace
		}

		// A stop Garrison asked for and completed is what lets a later
		// non-zero exit read as a shutdown rather than a crash.
		if m.Progress.Kind == tasks.KindStop && m.Progress.State == tasks.StateDone {
			srv.StopRequested = true
		}
		if m.Progress.Kind == tasks.KindStart && m.Progress.State != tasks.StateQueued {
			srv.StopRequested = false
		}
	}))
}

// busyFrom is what a server is currently having done to it, taken from the
// tasks in flight against it.
func busyFrom(s Snapshot, server string) Op {
	for _, t := range s.Tasks {
		if t.Server != server || t.State.Done() {
			continue
		}
		switch t.Kind {
		case tasks.KindStop:
			return OpStop
		case tasks.KindStart:
			return OpStart
		case tasks.KindRestart:
			return OpRestart
		case tasks.KindApplyConfig:
			return OpApply
		case tasks.KindUpdate:
			return OpUpdate
		case tasks.KindBackup:
			return OpBackup
		}
		return OpStart
	}
	return OpNone
}

// stopGraceFrom reads how long a stop task is waiting, which its first step
// revises once it has read the game's plan.
func stopGraceFrom(p tasks.Progress) (time.Duration, bool) {
	if p.Kind != tasks.KindStop && p.Kind != tasks.KindRestart {
		return 0, false
	}
	for i, name := range p.Steps {
		if name == "stop" && i < len(p.Est) && p.Est[i] > 0 {
			return p.Est[i], true
		}
	}
	return 0, false
}

// SettingEdited records one change the operator made but has not applied.
type SettingEdited struct {
	At     time.Time
	Server string
	Key    string
	Value  any
}

func (m SettingEdited) apply(s Snapshot) Snapshot {
	s.At = m.At
	return s.withServers(mapServer(s.Servers, m.Server, func(srv *Server) {
		draft := make(map[string]any, len(srv.Draft)+1)
		for k, v := range srv.Draft {
			draft[k] = v
		}
		draft[m.Key] = m.Value

		// An edit back to the configured value is not a change, and leaving
		// it in the draft would keep the apply badge lit forever over
		// nothing.
		if sameValue(m.Value, srv.Instance.Settings[m.Key]) {
			delete(draft, m.Key)
		}
		if len(draft) == 0 {
			draft = nil
		}
		srv.Draft = draft
	}))
}

// DraftDiscarded throws away unapplied changes.
type DraftDiscarded struct {
	At     time.Time
	Server string
}

func (m DraftDiscarded) apply(s Snapshot) Snapshot {
	s.At = m.At
	return s.withServers(mapServer(s.Servers, m.Server, func(srv *Server) {
		srv.Draft = nil
	}))
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

// BackupsListed is the archive poller reporting what is on disk for one
// server.
//
// It carries the whole list rather than a delta because that is what listing a
// directory produces, and because archives change from outside Garrison: a
// backup copied away by hand, or a directory restored from somewhere else,
// should be reflected on the next poll rather than drifting until a restart.
type BackupsListed struct {
	At      time.Time
	Server  string
	Archive []model.Archive
}

func (m BackupsListed) apply(s Snapshot) Snapshot {
	s.At = m.At
	return s.withServers(mapServer(s.Servers, m.Server, func(srv *Server) {
		// Newest first: a backups list is read from the top, and the one you
		// want after a bad update is almost always the most recent.
		sorted := make([]model.Archive, len(m.Archive))
		copy(sorted, m.Archive)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].Taken.After(sorted[j].Taken) })

		srv.Backups = sorted
		srv.BackupsKnown = true
	}))
}

// SessionsListed is the player tracker reporting a server's recent history.
//
// Like BackupsListed it carries the whole window rather than a delta: the
// tracker reads it back out of SQLite each time, so the snapshot and the
// database cannot drift apart between them.
type SessionsListed struct {
	At       time.Time
	Server   string
	Sessions []model.Session
}

func (m SessionsListed) apply(s Snapshot) Snapshot {
	s.At = m.At
	return s.withServers(mapServer(s.Servers, m.Server, func(srv *Server) {
		sorted := make([]model.Session, len(m.Sessions))
		copy(sorted, m.Sessions)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].Joined.After(sorted[j].Joined) })
		srv.Sessions = sorted
	}))
}

// InstanceAdded records a server the wizard just wrote.
//
// It is a separate mutation from InstancesLoaded because that one replaces the
// whole set — reloading the directory to pick up one new file would drop the
// draft settings and busy flags of every other server.
type InstanceAdded struct {
	At       time.Time
	Instance model.Instance
}

func (m InstanceAdded) apply(s Snapshot) Snapshot {
	s.At = m.At

	byName := make(map[string]model.Instance, len(s.instances)+1)
	for k, v := range s.instances {
		byName[k] = v
	}
	byName[m.Instance.Name] = m.Instance
	s.instances = byName

	return s.withServers(merge(s, observedFrom(s.Servers)))
}

// forget drops a server and its configuration from the snapshot.
//
// Both halves, because they are different sets: the instance is what the file
// said and the row is what Docker showed. A delete removes the file and the
// container, so leaving either behind would show a server that no longer
// exists in one place and not the other.
func (s Snapshot) forget(name string) Snapshot {
	if _, ok := s.instances[name]; ok {
		next := make(map[string]model.Instance, len(s.instances))
		for k, v := range s.instances {
			if k != name {
				next[k] = v
			}
		}
		s.instances = next
	}

	servers := make([]Server, 0, len(s.Servers))
	for _, srv := range s.Servers {
		if srv.Name != name {
			servers = append(servers, srv)
		}
	}
	return s.withServers(servers)
}

// RosterObserved is a server answering who is connected.
//
// It replaces the roster rather than merging into it, because that is what the
// answer means: a game that can be asked has told us the whole set, and a
// player missing from it has left. The log-derived roster for games that
// cannot be asked is built by rosterApply instead, and the two never run
// against the same server.
type RosterObserved struct {
	At      time.Time
	Server  string
	Players []model.Player
}

func (m RosterObserved) apply(s Snapshot) Snapshot {
	s.At = m.At
	return s.withServers(mapServer(s.Servers, m.Server, func(srv *Server) {
		players := make([]model.Player, len(m.Players))
		copy(players, m.Players)

		// Keep the arrival time we already had for anyone still on. The
		// answer says who is connected, not since when, and taking the
		// poll's own clock would restart every session on every poll.
		for i, p := range players {
			for _, known := range srv.Players {
				if known.Name == p.Name && !known.Since.IsZero() {
					players[i].Since = known.Since
					break
				}
			}
			if players[i].Since.IsZero() {
				players[i].Since = m.At
			}
		}

		srv.Players = players
		// A roster that was asked for supersedes anything inferred, so the
		// half-identified connections the log-derived path tracks are no
		// longer meaningful for this server.
		srv.connecting = nil
	}))
}
