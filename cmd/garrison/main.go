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
	"github.com/camden-brown/garrison/internal/services/fleet"
	"github.com/camden-brown/garrison/internal/services/logs"
	"github.com/camden-brown/garrison/internal/services/metrics"
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
		return fmt.Errorf("unknown command %q: try status, version, or no argument for the dashboard", cmd)
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
	engine := tasks.New(driver, resolver{store: store}, store, journal)
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
	)

	prune := func() {
		now := time.Now()
		_, _ = db.Journal().Prune(now.Add(-history))
		_, _ = db.PruneMetrics(now.Add(-metrics))
		_, _ = db.PruneEvents(now.Add(-events))
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

// schedule is the scheduler's window onto the store plus the configured jobs.
type schedule struct {
	store *core.Store
	jobs  []scheduler.Job
}

func (s schedule) Players(server string) (int, bool) { return s.store.Players(server) }
func (s schedule) Schedules() []scheduler.Job        { return s.jobs }

func (s schedule) Submit(ctx context.Context, server string, kind tasks.Kind, trigger tasks.Trigger) {
	s.store.SubmitScheduled(ctx, server, kind, trigger)
}

func (s schedule) Notify(ctx context.Context, server, text string) {
	s.store.Notify(ctx, server, text)
}

// resolver tells the task engine what a server is. It reads the store rather
// than the config directory so a task acts on the same picture the screen is
// showing, including settings applied a moment ago.
type resolver struct{ store *core.Store }

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
