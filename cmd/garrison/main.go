// Command garrison is a terminal dashboard for running game servers in Docker.
//
// With no arguments it opens the TUI. Every action in the TUI is also a
// subcommand, so a scheduled job can reach it — the TUI and the CLI are peers
// over internal/core, not wrappers around each other.
//
// This is M0: the Docker driver, the store, and a Fleet view that lists
// labelled containers and can start and stop them. See docs/DESIGN.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/camden-brown/garrison/internal/config"
	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/games"
	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/host/docker"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/services/backup"
	"github.com/camden-brown/garrison/internal/services/command"
	"github.com/camden-brown/garrison/internal/services/conn"
	"github.com/camden-brown/garrison/internal/services/fleet"
	"github.com/camden-brown/garrison/internal/services/logs"
	"github.com/camden-brown/garrison/internal/services/metrics"
	"github.com/camden-brown/garrison/internal/services/mods"
	"github.com/camden-brown/garrison/internal/services/players"
	"github.com/camden-brown/garrison/internal/services/scheduler"
	sqlitestore "github.com/camden-brown/garrison/internal/store"
	"github.com/camden-brown/garrison/internal/tasks"
	"github.com/camden-brown/garrison/internal/tui"
	"github.com/camden-brown/garrison/internal/tui/comp"
	viewsall "github.com/camden-brown/garrison/internal/tui/views/all"

	// Registers every game. Adding one is a line in that package.
	_ "github.com/camden-brown/garrison/internal/games/all"
)

// version is set at build time: -ldflags "-X main.version=$(git describe --tags)"
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		// The one place errors are printed. Everything below returns them.
		fmt.Fprintf(os.Stderr, "garrison: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("garrison", flag.ContinueOnError)
	confDir := fs.String("config", defaultConfigDir(), "directory holding garrison.toml and servers/")
	endpoint := fs.String("docker-endpoint", "",
		"Docker endpoint (default: npipe:////./pipe/docker_engine on Windows, unix:///var/run/docker.sock elsewhere)")
	interval := fs.Duration("interval", fleet.DefaultInterval, "how often to re-list the fleet")
	ascii := fs.Bool("ascii", false, "replace box drawing and block elements with plain characters")
	timeout := fs.Duration("timeout", 10*time.Minute, "how long a subcommand waits for its task before giving up")
	if err := fs.Parse(args); err != nil {
		return err
	}

	switch cmd := fs.Arg(0); cmd {
	case "", "fleet":
		return runTUI(*confDir, *endpoint, *interval, *ascii)
	case "status":
		return runStatus(*confDir, *endpoint, *interval)
	case "version":
		printVersion()
		return nil
	default:
		// Every action in the TUI is also a subcommand. runVerb reports an
		// unknown one, so there is one place that knows the whole set.
		return runVerb(cmd, *confDir, *endpoint, *interval, *timeout, fs.Args()[1:])
	}
}

// setup resolves the endpoint, wires the store to the driver, and starts the
// writer and the poller.
//
// This is the only function that knows both halves of the program exist. The
// store never imports a service and no service imports the store; they meet
// here, which is what keeps the dependency rule true rather than aspirational.
// defaultConfigDir is %APPDATA%\Garrison on Windows and the XDG config
// directory elsewhere, which is where a person would look for it on each.
func defaultConfigDir() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "Garrison")
	}
	return "garrison"
}

func setup(ctx context.Context, confDir, endpoint string, interval time.Duration) (*core.Store, *sync.WaitGroup, error) {
	driver, err := docker.Open(endpoint, docker.Env{
		Garrison: os.Getenv("GARRISON_DOCKER_HOST"),
		Docker:   os.Getenv("DOCKER_HOST"),
	})
	if err != nil {
		return nil, nil, err
	}

	resolved, _ := driver.Endpoint()
	servers := config.Store{Dir: filepath.Join(confDir, "servers")}

	// The engine and the store need each other: the store submits tasks and
	// the engine reports progress back. cmd is where that knot is tied,
	// which is the same reason it is the only place the driver and the
	// views are both named.
	// The database is optional in the sense that everything works without
	// it — the journal falls back to a nop and nothing survives a restart.
	// It is not optional in the sense of being skipped quietly: a failure to
	// open it is reported and the dashboard carries on, because a dashboard
	// that will not start because its history file is unwritable is a
	// dashboard missing when you need it.
	var journal tasks.Journal
	var dbErr error
	db, dbErr := sqlitestore.Open(filepath.Join(confDir, "garrison.db"))
	if dbErr == nil {
		journal = db.Journal()
	}

	// The archive store needs to read the fleet to find a server's data
	// directory, and the fleet lives in the store being constructed. The
	// indirection is a pointer filled in immediately below rather than a
	// nil that would surface as a confusing failure the first time somebody
	// pressed the backup key.
	arch := &archives{}
	store := core.New(core.Options{
		MetricLabels: metricLabels(),
		Saver:        servers,
		Archives:     arch,
		KeepBackups:  defaultKeepBackups,
	})
	arch.store = store
	pool := &conn.Pool{Driver: driver}
	res := resolver{store: store, pool: pool}
	commands := command.New(boundCommand{res: res})
	store.AttachCommander(commands)

	engine := tasks.New(driver, res, store, journal)
	store.AttachTasks(engine)

	// The two streamers follow containers; the poller tells everyone what
	// exists. Neither service imports the store and the store imports
	// neither of them — this is the only place all three are named, which
	// is what ADR 0007 buys.
	// The config directory is read once at startup. Watching it for changes
	// is a later convenience; today an edit made with the tool open is
	// picked up on the next launch, which the file being hand-editable is
	// the whole point of.
	instances, problems := servers.LoadAll()
	store.InstancesLoaded(ctx, instances)
	for _, err := range problems {
		store.Send(ctx, core.NoticeRaised{At: time.Now(), Level: core.LevelError, Text: err.Error()})
	}
	if dbErr != nil {
		store.Send(ctx, core.NoticeRaised{
			At: time.Now(), Level: core.LevelError,
			Text: "history is not being kept: " + dbErr.Error(),
		})
	}

	stats := metrics.NewStreamer(driver, store)
	console := logs.NewStreamer(driver, parsers, store)
	observer := fanOut{store: store, stats: stats, logs: console}

	var wg sync.WaitGroup
	run := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f()
		}()
	}

	run(func() { store.Run(ctx) })
	run(func() { engine.Run(ctx) })
	if db != nil {
		run(func() {
			housekeep(ctx, db)
			db.Close()
		})
	}
	run(func() { stats.Run(ctx) })
	run(func() { console.Run(ctx) })
	run(func() {
		poller := &fleet.Poller{Driver: driver, Interval: interval}
		poller.Run(ctx, observer)
	})

	// The archive poller is what makes the Backups screen a list rather than
	// a promise. It is separate from the fleet poll because it reads the
	// filesystem rather than the engine, and because backups change on a
	// scale of minutes rather than seconds.
	run(func() {
		(&backup.Poller{Interval: backup.DefaultInterval, Dirs: arch}).Run(ctx, store)
	})

	// Console commands and the roster both talk over RCON, so both go
	// through the connection pool and both stop when the context does. The
	// pool is closed last so a command in flight is not cut off mid-reply.
	run(func() {
		commands.Run(ctx, store)
	})
	// Mod resolution is hourly and entirely for drawing a badge, so it is
	// the slowest thing here by a wide margin.
	run(func() {
		(&mods.Poller{
			Interval: mods.DefaultInterval,
			Refs:     modRefs{store: store},
			Sources:  modSources{store: store},
		}).Run(ctx, store)
	})
	run(func() {
		(&players.RosterPoller{
			Interval: players.DefaultRosterInterval,
			Servers:  fleetNames{store: store},
			Asker:    boundRoster{res: res},
		}).Run(ctx, store)
	})
	run(func() {
		<-ctx.Done()
		_ = pool.Close()
	})

	// Session history needs somewhere durable, so it runs only when the
	// database opened. Without it the Players view shows who is on now and
	// says the history is not being kept, which is the same failure the
	// notice above already reported.
	if db != nil {
		if closed, err := db.CloseStaleSessions(time.Now()); err == nil && closed > 0 {
			store.Send(ctx, core.NoticeRaised{
				At: time.Now(), Level: core.LevelInfo,
				Text: fmt.Sprintf("closed %d session(s) left open by a previous run", closed),
			})
		}
		run(func() {
			(&players.Tracker{
				Interval: players.DefaultInterval,
				Window:   players.DefaultWindow,
				Fleet:    rosters{store: store},
				Store:    db,
			}).Run(ctx, store)
		})
	}

	// The scheduler is started last and seeded first, so opening Garrison
	// at ten in the morning does not immediately run the restart that was
	// due at four.
	jobs, badCron := scheduler.JobsFrom(instances)
	for _, err := range badCron {
		store.Send(ctx, core.NoticeRaised{At: time.Now(), Level: core.LevelError, Text: err.Error()})
	}
	if len(jobs) > 0 {
		sched := scheduler.New(schedule{store: store, jobs: jobs})
		sched.Seed()
		run(func() { sched.Run(ctx) })
	}

	store.Send(ctx, core.EngineResolved{
		At:        time.Now(),
		Endpoint:  resolved,
		Transport: docker.Transport(resolved),
		Poll:      interval,
	})

	return store, &wg, nil
}

// housekeep trims the database on a slow timer.
//
// Every buffer in Garrison is bounded and the database is no exception: the
// task journal, the cold metrics and the event log all have a horizon, and a
// process left open for weeks is exactly the one that would otherwise find out
// they do not.
func housekeep(ctx context.Context, db *sqlitestore.DB) {
	const (
		every   = 6 * time.Hour
		metrics = 30 * 24 * time.Hour // DESIGN §8: the cold tier is 30 days
		events  = 90 * 24 * time.Hour // DESIGN §8: events are kept 90 days
		history = 90 * 24 * time.Hour

		// Sessions outlive the Players view's seven-day window so a longer
		// one can be asked for later without the data already being gone.
		sessions = 90 * 24 * time.Hour
	)

	prune := func() {
		now := time.Now()
		_, _ = db.Journal().Prune(now.Add(-history))
		_, _ = db.PruneMetrics(now.Add(-metrics))
		_, _ = db.PruneEvents(now.Add(-events))
		_, _ = db.PruneSessions(now.Add(-sessions))
	}
	prune()

	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}

// defaultKeepBackups is how many archives survive per server until the wizard
// asks. Fourteen is two weeks of nightlies, which is long enough to notice a
// world has gone wrong and short enough not to fill a disk unattended.
const defaultKeepBackups = 14

// archives gives each server its own backup directory, beside its data.
//
// DESIGN §11 puts backups next to the world they came from rather than in a
// central place, so moving a server means moving one directory.
type archives struct{ store *core.Store }

func (a *archives) For(server string) tasks.Archiver {
	return &serverArchive{store: a.store, server: server}
}

type serverArchive struct {
	store  *core.Store
	server string
}

func (s *serverArchive) dir() string {
	if s.store == nil {
		return ""
	}
	srv, ok := s.store.Snapshot().Server(s.server)
	if !ok || srv.Instance.Data == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(srv.Instance.Data), "backups")
}

func (s *serverArchive) Create(ctx context.Context, src string, at time.Time) (string, int64, error) {
	dir := s.dir()
	if dir == "" {
		return "", 0, fmt.Errorf("%s: no data directory configured, so there is nowhere to put a backup", s.server)
	}
	a, err := backup.Store{Dir: dir}.Create(ctx, src, at)
	if err != nil {
		return "", 0, err
	}
	return a.Path, a.Bytes, nil
}

func (s *serverArchive) Prune(keep int) ([]string, error) {
	dir := s.dir()
	if dir == "" {
		return nil, nil
	}
	return backup.Store{Dir: dir}.Prune(keep)
}

// Restore unpacks an archive over the server's data directory. The service
// refuses to write outside dst, which is what makes an archive from anywhere
// safe to accept.
func (s *serverArchive) Restore(ctx context.Context, archive, dst string) error {
	dir := s.dir()
	if dir == "" {
		return fmt.Errorf("%s: no data directory configured, so there is nowhere to restore to", s.server)
	}
	return backup.Store{Dir: dir}.Restore(ctx, archive, dst)
}

// BackupDirs answers the archive poller: every configured server, and where
// its archives live.
//
// This is the same directory serverArchive.dir computes and for the same
// reason — it is derived from the configured data directory, which only the
// store knows. Both sides of that live here because cmd is where the store and
// the services are allowed to meet (ADR 0007).
func (a *archives) BackupDirs() map[string]string {
	if a.store == nil {
		return nil
	}
	snap := a.store.Snapshot()
	out := make(map[string]string, len(snap.Servers))
	for _, srv := range snap.Servers {
		dir := (&serverArchive{store: a.store, server: srv.Name}).dir()
		if dir != "" {
			out[srv.Name] = dir
		}
	}
	return out
}

// rosters answers the player tracker: who is connected to each server right
// now, as the store currently understands it.
//
// The tracker diffs this rather than reading log events, so it works the same
// for a game whose roster is inferred from a log and one that will answer over
// RCON. cmd supplies it because a service never imports core (ADR 0007).
type rosters struct{ store *core.Store }

func (r rosters) Rosters() map[string][]model.Player {
	if r.store == nil {
		return nil
	}
	snap := r.store.Snapshot()
	out := make(map[string][]model.Player, len(snap.Servers))
	for _, srv := range snap.Servers {
		out[srv.Name] = srv.Players
	}
	return out
}

// schedule is the scheduler's window onto the store plus the configured jobs.
type schedule struct {
	store *core.Store
	jobs  []scheduler.Job
}

func (s schedule) Players(server string) (int, bool) { return s.store.Players(server) }
func (s schedule) Schedules() []scheduler.Job        { return s.jobs }

func (s schedule) Submit(ctx context.Context, server string, kind tasks.Kind, trigger tasks.Trigger, drain time.Duration) {
	s.store.SubmitScheduled(ctx, server, kind, trigger, drain)
}

func (s schedule) Notify(ctx context.Context, server, text string) {
	s.store.Notify(ctx, server, text)
}

// resolver tells the task engine what a server is. It reads the store rather
// than the config directory so a task acts on the same picture the screen is
// showing, including settings applied a moment ago.
type resolver struct {
	store *core.Store
	pool  *conn.Pool
}

// Drainer binds a game's drain capability to a live transport.
//
// This adaptation is why the method is here rather than in internal/tasks:
// tasks must not import internal/games, because internal/store depends on
// tasks and the dependency rule forbids anything under store from reaching
// games. cmd is the one place allowed to know both (ADR 0007), so cmd is
// where a games.Drainable becomes a tasks.Drainer.
//
// Nil for a stopped container, a game with no channel, or a server whose RCON
// is not configured — none of which is an error. A drain that cannot warn
// degrades to a restart that does not, and says so in the task's history.
func (r resolver) Drainer(server string) tasks.Drainer {
	c, g := r.transport(server)
	if c == nil {
		return nil
	}
	drainable, ok := g.(games.Drainable)
	if !ok {
		return nil
	}
	return boundDrain{conn: c, game: drainable}
}

// transport is the shared half of Drainer and the command runner: a live
// connection for a server, or nil.
func (r resolver) transport(server string) (*conn.Conn, games.Game) {
	if r.pool == nil || r.store == nil {
		return nil, nil
	}
	srv, ok := r.store.Snapshot().Server(server)
	if !ok || srv.ID == "" {
		return nil, nil
	}

	g, err := games.Get(srv.Game)
	if err != nil {
		return nil, nil
	}
	plan, err := g.Plan(srv.Instance)
	if err != nil || !plan.HasRCON() {
		return nil, nil
	}

	return r.pool.Get(host.Container{
		ID: srv.ID, Instance: srv.Name, Game: srv.Game, Ports: srv.Ports,
	}, plan), g
}

// boundCommand adapts a games.Commandable for internal/services/command, and
// boundRoster a games.Rostered for the roster poller. Both exist for the
// reason boundDrain does: those packages must not import internal/games, and
// cmd is the one place allowed to know both sides.
type boundCommand struct{ res resolver }

func (b boundCommand) Run(ctx context.Context, server, cmd string) (string, bool, error) {
	c, g := b.res.transport(server)
	if c == nil {
		return "", false, nil
	}
	commandable, ok := g.(games.Commandable)
	if !ok {
		return "", false, nil
	}

	ctx, cancel := conn.WithTimeout(ctx)
	defer cancel()

	out, err := commandable.Command(ctx, c, cmd)
	return out, true, err
}

type boundRoster struct{ res resolver }

func (b boundRoster) Ask(ctx context.Context, server string) ([]model.Player, bool, error) {
	c, g := b.res.transport(server)
	if c == nil {
		return nil, false, nil
	}
	rostered, ok := g.(games.Rostered)
	if !ok {
		return nil, false, nil
	}

	ctx, cancel := conn.WithTimeout(ctx)
	defer cancel()

	out, err := rostered.Roster(ctx, c)
	return out, true, err
}

// modRefs and modSources are the mod resolver's two halves: what each server
// has configured, and which resolver can answer for it.
//
// The source comes from asserting games.Moddable and reading ModSource, which
// is the assertion that keeps a list of which games have mods out of the
// service — and out of everywhere else.
type modRefs struct{ store *core.Store }

func (m modRefs) ModRefs() map[string][]model.ModRef {
	if m.store == nil {
		return nil
	}
	snap := m.store.Snapshot()
	out := make(map[string][]model.ModRef, len(snap.Servers))
	for _, srv := range snap.Servers {
		out[srv.Name] = srv.Instance.Mods
	}
	return out
}

type modSources struct{ store *core.Store }

func (m modSources) For(server string) mods.Resolver {
	if m.store == nil {
		return nil
	}
	srv, ok := m.store.Snapshot().Server(server)
	if !ok {
		return nil
	}
	g, err := games.Get(srv.Game)
	if err != nil {
		return nil
	}
	moddable, ok := g.(games.Moddable)
	if !ok {
		return nil
	}

	switch moddable.ModSource() {
	case games.ModSourceWorkshop:
		return mods.Workshop{}
	default:
		// A source Garrison has no resolver for. The Mods screen shows the
		// configured list unresolved, which is honest — nobody has looked.
		return nil
	}
}

// fleetNames is the set the roster poller walks.
type fleetNames struct{ store *core.Store }

func (f fleetNames) Names() []string {
	if f.store == nil {
		return nil
	}
	snap := f.store.Snapshot()
	out := make([]string, 0, len(snap.Servers))
	for _, srv := range snap.Servers {
		out = append(out, srv.Name)
	}
	return out
}

// boundDrain is a games.Drainable with its transport attached.
type boundDrain struct {
	conn *conn.Conn
	game games.Drainable
}

func (b boundDrain) Warn(ctx context.Context, in time.Duration) error {
	return b.game.Warn(ctx, b.conn, in)
}

func (b boundDrain) Save(ctx context.Context) error {
	return b.game.Save(ctx, b.conn)
}

func (r resolver) Instance(server string) (model.Instance, tasks.Game, error) {
	srv, ok := r.store.Snapshot().Server(server)
	if !ok {
		return model.Instance{}, nil, fmt.Errorf("%s: no such server", server)
	}

	inst := srv.Instance
	if inst.Name == "" {
		// A container found by label with no config file. It can still be
		// started and stopped; it just has no settings to compile.
		inst = model.Instance{Name: srv.Name, Game: srv.Game}
	}

	g, err := games.Get(inst.Game)
	if err != nil {
		return inst, nil, fmt.Errorf("%s: %w", server, err)
	}
	return inst, g, nil
}

// fanOut sends each poll to everything that needs to know what is running.
//
// The store wants it to rebuild the fleet; the streamers want it to open and
// close their per-container goroutines. Doing the fan-out here rather than
// chaining the services keeps each of them unaware of the others.
type fanOut struct {
	store *core.Store
	stats *metrics.Streamer
	logs  *logs.Streamer
}

func (f fanOut) FleetObserved(ctx context.Context, at time.Time, containers []host.Container) {
	f.store.FleetObserved(ctx, at, containers)
	f.stats.Reconcile(containers)
	f.logs.Reconcile(containers)
}

func (f fanOut) HostDescribed(ctx context.Context, at time.Time, info host.Info) {
	f.store.HostDescribed(ctx, at, info)
}

func (f fanOut) FleetUnobservable(ctx context.Context, at time.Time, err error) {
	f.store.FleetUnobservable(ctx, at, err)
	// The engine is unreachable, so every stream is already failing. Telling
	// the streamers the fleet is empty stops them cleanly rather than
	// leaving goroutines reading from a socket that has gone.
	f.stats.Reconcile(nil)
	f.logs.Reconcile(nil)
}

// parsers resolves a game id to its line parser, backed by the plugin
// registry. It is a function so internal/services/logs does not import it.
func parsers(game string) (func(string) model.Event, bool) {
	g, err := games.Get(game)
	if err != nil {
		return nil, false
	}
	return g.Parse, true
}

// metricLabels is what each game calls its fourth dashboard tile.
func metricLabels() map[string]string {
	out := map[string]string{}
	for _, g := range games.All() {
		m := g.Meta()
		if m.MetricLabel != "" {
			out[m.ID] = m.MetricLabel
		}
	}
	return out
}

func runTUI(confDir, endpoint string, interval time.Duration, ascii bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	store, wg, err := setup(ctx, confDir, endpoint, interval)
	if err != nil {
		return err
	}

	comp.ColorFromEnv()
	app := tui.NewApp(ctx, store, comp.NewTheme(ascii), viewsall.Views()...)

	_, err = tea.NewProgram(app, tea.WithAltScreen(), tea.WithContext(ctx)).Run()

	// Bring the background goroutines down before returning, so a leak shows
	// up here as a hang rather than as a process that quietly outlives its
	// own UI.
	stop()
	wg.Wait()

	if err != nil && !errors.Is(err, tea.ErrProgramKilled) && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// runStatus is the CLI peer of the fleet view: one line per server, for a
// scheduled job or a stream-deck button. It waits for one poll and prints it.
func runStatus(confDir, endpoint string, interval time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	store, wg, err := setup(ctx, confDir, endpoint, interval)
	if err != nil {
		return err
	}
	defer wg.Wait()
	defer cancel()

	sub := store.Subscribe()
	for {
		select {
		case <-ctx.Done():
			return errors.New("timed out waiting for the container engine")
		case snap, ok := <-sub:
			if !ok {
				return errors.New("store shut down before the first poll")
			}
			if snap.Engine.LastOK.IsZero() && snap.Engine.Err == "" {
				continue // nothing observed yet
			}
			if !snap.Engine.OK {
				return fmt.Errorf("%s: %s", snap.Engine.Transport, snap.Engine.Err)
			}
			printStatus(snap)
			return nil
		}
	}
}

func printStatus(snap core.Snapshot) {
	if len(snap.Servers) == 0 {
		fmt.Println("no containers labelled garrison.managed=1")
		return
	}
	now := time.Now()
	for _, srv := range snap.Servers {
		line := fmt.Sprintf("%-24s %-10s %-10s %s",
			srv.Name, srv.Game, srv.State, comp.Duration(srv.Uptime(now)))
		if srv.Detail != "" {
			line += "  " + srv.Detail
		}
		fmt.Println(line)
	}
}

func printVersion() {
	fmt.Printf("garrison %s  %s/%s\n", version, runtime.GOOS, runtime.GOARCH)

	ids := games.IDs()
	if len(ids) == 0 {
		fmt.Println("\nno games registered yet — next is internal/games/valheim (M1).")
		return
	}

	fmt.Printf("\n%d game(s) registered:\n", len(ids))
	for _, g := range games.All() {
		m := g.Meta()
		fmt.Printf("  %-10s %-22s steam %s\n", m.ID, m.Name, m.SteamAppID)
		if caps := capabilities(g); len(caps) > 0 {
			fmt.Printf("             %s\n", strings.Join(caps, " · "))
		} else {
			fmt.Println("             (no optional capabilities)")
		}
	}
}

// capabilities reports which optional interfaces a game implements. The same
// assertions gate views in the TUI, which is how a game with no mods gets an
// explanation instead of an empty table.
func capabilities(g games.Game) []string {
	var out []string
	if _, ok := g.(games.Rostered); ok {
		out = append(out, "rostered")
	}
	if _, ok := g.(games.Commandable); ok {
		out = append(out, "commandable")
	}
	if _, ok := g.(games.Drainable); ok {
		out = append(out, "drainable")
	}
	if _, ok := g.(games.Moddable); ok {
		out = append(out, "moddable")
	}
	if _, ok := g.(games.Backupable); ok {
		out = append(out, "backupable")
	}
	if _, ok := g.(games.Probeable); ok {
		out = append(out, "probeable")
	}
	return out
}
