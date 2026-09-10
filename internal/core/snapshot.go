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
	"strconv"
	"time"

	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tasks"
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

	// Tasks is everything the engine is doing or has recently done, newest
	// last. Bounded like everything else here.
	Tasks []tasks.Progress

	// instances is what the config files say, keyed by name. It is kept
	// separately from Servers because the two are different sets: a server
	// can be configured and not yet created, or running and not configured
	// at all — Garrison finds containers by label, so it will meet servers
	// it has no file for.
	instances map[string]model.Instance
}

// Engine is what Garrison knows about the container runtime it is talking to.
type Engine struct {
	Endpoint  string // "npipe:////./pipe/docker_engine"
	Transport string // "npipe", "unix" — what the status bar shows
	OK        bool
	Err       string    // why not, when OK is false
	LastOK    time.Time // when it last answered, so "down for 4m" is sayable

	// Poll is how often the fleet is re-listed, so the status bar can say
	// how fresh what you are looking at is.
	Poll time.Duration

	// The host's own figures, from the engine. Capacity is what turns usage
	// into a proportion: 27 GiB means nothing until you know the machine
	// has 64.
	Version  string
	OS       string
	NCPU     int
	MemTotal int64
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

	// CPU and Mem are the sampled histories behind the dashboard's
	// sparklines and live numbers. They are empty until the server is up and
	// a stats stream has been open for a second.
	CPU model.History
	Mem model.History

	// MemLimit is what the container is capped at, for the "6.1 / 8 GiB"
	// reading. Zero means uncapped, which is a bad idea for a game server
	// and something the wizard never proposes.
	MemLimit int64

	// Net is combined network throughput in bytes per second, computed from
	// the deltas between samples. Docker reports cumulative counters, which
	// as a sparkline is a straight line going up and tells you nothing.
	Net model.History

	// netTotal is the previous cumulative reading, kept so the next sample
	// can be turned into a rate. Unexported: it is arithmetic scaffolding
	// rather than something a view should render.
	netTotal int64

	// Console is the server's recent output, oldest first and bounded to
	// model.ConsoleCap. It shares its storage between snapshots rather than
	// copying on write, which is what makes sixteen thousand lines
	// affordable — see the note on model.Ring, which explains why that is
	// safe here and would not be in general.
	Console model.Ring

	// Players is who is connected, as reconstructed from log events for the
	// games that offer no roster endpoint.
	Players []model.Player

	// Backups is the server's archives, newest first, as last listed by the
	// poller. Empty is ambiguous on purpose only until BackupsListed has
	// run once — see BackupsKnown.
	Backups []model.Archive

	// Mods is the configured mods as resolved: the TOML's order, with
	// whatever a resolver found out about each. Empty for a game with no
	// mod system, and unresolved — names and versions blank — until a
	// resolver has run.
	Mods []model.Mod

	// Sessions is who has played recently, newest first, as far back as the
	// tracker's window. Open sessions — players still connected — have a
	// zero Left, which model.Session.Open reports.
	Sessions []model.Session

	// BackupsKnown says the poller has answered at least once, so an empty
	// Backups means "none" rather than "not looked yet". A view that cannot
	// tell those apart says "no backups" about a directory it has never
	// read.
	BackupsKnown bool

	// connecting holds clients that have attached but not yet named
	// themselves, oldest first. See the note on rosterApply.
	connecting []string

	// Instance is the configured form: image, ports, resources, settings.
	// Zero when Configured is false, which happens for a container found by
	// label that Garrison has no file for — a fleet recovered after losing
	// the config directory, or one somebody created by hand.
	Instance   model.Instance
	Configured bool

	// Draft holds edited settings not yet applied, keyed the game's own way.
	// Nil means nothing is pending.
	//
	// It lives in the store rather than in the settings view because
	// applying it is a task, and a task cannot reach into a view. It also
	// means the pending count is visible from anywhere — the fleet row can
	// say a server has unsaved changes without the settings screen being
	// open.
	Draft map[string]any

	// Created reports whether a container exists. A configured server that
	// has never been created is a real thing to show: it is the difference
	// between "stopped" and "not built yet".
	Created bool

	// GameMetric is the fourth dashboard tile, and MetricLabel names it.
	//
	// Both come from the game plugin rather than from anything here: its
	// Parse emits an Event carrying a metric name and a value, and the tile
	// renders whatever that is. It is how Zomboid shows zombies alive and
	// Valheim world-save duration without a line of per-game code in the
	// TUI. An empty MetricLabel means the game supplies none and the tile
	// shows network I/O instead.
	GameMetric  model.History
	MetricLabel string

	// StopGrace is how long the stop in flight will wait before killing, and
	// afterwards how long the one that killed it waited. It is what lets both
	// "stopping… up to 60s" and "killed after 60s grace" name a real number
	// instead of gesturing at one.
	StopGrace time.Duration

	// StopRequested records that Garrison asked this server to stop and the
	// request succeeded.
	//
	// It is the difference between a crash and a shutdown, which the exit
	// code alone cannot tell you: a server that ignores its stop signal is
	// killed after the grace period and exits 137, exactly like one the
	// kernel killed. Only Garrison knows which of those it asked for.
	StopRequested bool
}

// Setting is the value a key would have if the draft were applied: the edit
// if there is one, otherwise what is configured.
func (s Server) Setting(key string) (any, bool) {
	if v, ok := s.Draft[key]; ok {
		return v, true
	}
	v, ok := s.Instance.Settings[key]
	return v, ok
}

// Edited reports whether a key has an unapplied change that differs from what
// is configured. An edit back to the original value is not a change.
func (s Server) Edited(key string) bool {
	v, ok := s.Draft[key]
	if !ok {
		return false
	}
	return !sameValue(v, s.Instance.Settings[key])
}

// Pending is how many settings differ from what is configured.
func (s Server) Pending() int {
	n := 0
	for key := range s.Draft {
		if s.Edited(key) {
			n++
		}
	}
	return n
}

// sameValue compares two setting values.
//
// TOML decodes an integer as int64 while a form produces an int, so a value
// edited back to what it started as would otherwise read as changed forever —
// and the apply badge would never clear.
func sameValue(a, b any) bool {
	if a == nil || b == nil {
		return a == b
	}
	if x, ok := asInt(a); ok {
		if y, ok := asInt(b); ok {
			return x == y
		}
	}
	if x, ok := asFloat(a); ok {
		if y, ok := asFloat(b); ok {
			return x == y
		}
	}
	return a == b
}

func asInt(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	}
	return 0, false
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float32:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
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
	OpNone    Op = ""
	OpStart   Op = "start"
	OpStop    Op = "stop"
	OpRestart Op = "restart"
	OpApply   Op = "apply"
	OpUpdate  Op = "update"
	OpBackup  Op = "backup"
	OpRestore Op = "restore"
	OpDelete  Op = "delete"
)

// Present is the progressive form, which is what a busy row says.
func (o Op) Present() string {
	switch o {
	case OpStart:
		return "starting"
	case OpStop:
		return "stopping"
	case OpRestart:
		return "restarting"
	case OpApply:
		return "applying settings"
	case OpUpdate:
		return "updating"
	case OpBackup:
		return "backing up"
	case OpRestore:
		return "restoring"
	case OpDelete:
		return "deleting"
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

// maxTasks bounds the task list. Long-term history belongs in SQLite, which
// answers "what happened last Tuesday"; this answers "what is happening now
// and what just finished", and those want very different sizes.
const maxTasks = 50

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

// Task returns one task by id.
func (s Snapshot) Task(id string) (tasks.Progress, bool) {
	for _, t := range s.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return tasks.Progress{}, false
}

// RunningTasks is what is in flight right now.
func (s Snapshot) RunningTasks() []tasks.Progress {
	var out []tasks.Progress
	for _, t := range s.Tasks {
		if !t.State.Done() {
			out = append(out, t)
		}
	}
	return out
}

// TaskFor is the task in flight against a server, if there is one. The fleet
// row uses it to say what is happening rather than only that something is.
func (s Snapshot) TaskFor(server string) (tasks.Progress, bool) {
	for _, t := range s.Tasks {
		if t.Server == server && !t.State.Done() {
			return t, true
		}
	}
	return tasks.Progress{}, false
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

// Budget renders a stop grace the way an operator reads a short wait: whole
// seconds up to a couple of minutes, then minutes.
//
// It lives here rather than in the TUI because the snapshot already carries
// human-readable Detail strings — that is what makes a view a pure function of
// one — and both the in-flight message and the after-the-fact reason have to
// name the same number the same way.
func Budget(d time.Duration) string {
	d = d.Round(time.Second)
	if d <= 0 {
		return "0s"
	}
	if d < 2*time.Minute {
		return strconv.Itoa(int(d.Seconds())) + "s"
	}

	m := int(d.Minutes())
	if rem := int(d.Seconds()) % 60; rem != 0 {
		return strconv.Itoa(m) + "m" + strconv.Itoa(rem) + "s"
	}
	return strconv.Itoa(m) + "m"
}
