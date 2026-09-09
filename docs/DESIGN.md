# Garrison — design

Status: design complete, implementation at M0. This document is the durable
copy of the engineering decisions; the visual design (nine annotated terminal
mockups at 120x34, the keymap, the colour system) lives in a separate document.

## Contents

- [1. Shape and non-goals](#1-shape-and-non-goals)
- [2. Navigation model](#2-navigation-model)
- [3. Screens](#3-screens)
- [4. Visual system](#4-visual-system)
- [5. Architecture](#5-architecture)
- [6. The game interface](#6-the-game-interface)
- [7. Keeping it maintainable](#7-keeping-it-maintainable)
- [8. Data pipelines](#8-data-pipelines)
- [9. Task engine](#9-task-engine)
- [10. Settings](#10-settings)
- [11. On disk](#11-on-disk)
- [12. Build order](#12-build-order)
- [13. Risks and open questions](#13-risks-and-open-questions)

## 1. Shape and non-goals

Garrison is a single Go binary that runs in Windows Terminal and treats every
game server as a Docker container it owns. You leave it open. It shows what is
happening, and it is the place you change things.

The problem it solves is not "start a game server" — a `docker run` line does
that. It is that every new game means re-learning a config format, re-deriving
how to read its logs, re-writing a restart script, and rebuilding somewhere to
see if it is healthy. Garrison makes that work a plugin.

**Non-goals.** No web panel, no auth model, no browser: remote access is SSH to
the box. No duplication of state Docker already holds. No multi-user story.

The binary is `garrison`, aliased `gar`. With no arguments it opens the TUI.
Every action also exists as a subcommand (`gar restart zomboid-main --drain
15m`) because the day you want it in Task Scheduler you will want it badly. The
TUI and the CLI are peers over `internal/core`; neither wraps the other.

## 2. Navigation model

Two axes, never nested deeper than two: **which server**, and **which view of
it**. The rail down the left holds both, stacked — a server list on top, a view
list below. The stage fills the rest. A one-line status bar across the bottom
never changes position: fleet health, running task count, refresh state, clock,
and the two or three keys that matter right now.

Above the server list is a **Fleet** entry, the only screen not about one
server. It is the default on launch and the thing you leave up.

`Tab` moves focus between rail·servers, rail·views and the stage. Getting
anywhere takes one keystroke: `1`–`7` jump to a view of the current server and
never change meaning; `Ctrl+P` is a fuzzy palette over servers, views and verbs
(`zom rest` finds "restart zomboid-main"); `:` is a command line for what a
palette is clumsy at (`:drain 15m`, `:mods add 2822286426`); `/` filters the
current view, always incremental, `Esc` clears.

**Destructive actions are shaped differently.** Lowercase keys are safe and
immediate; uppercase keys open a confirm modal. Anything that can lose a world
— deleting a server, a `WipeRisk` setting, restoring a backup over a live save
— requires typing the server's name. That is deliberate friction in exactly
three places and nowhere else.

## 3. Screens

Nine, drawn at 120x34 and all degrading to 80 columns.

| Screen | Key | Job |
| --- | --- | --- |
| Fleet | `f` | Every server on one line; the always-on view |
| Dashboard | `1` | Four tiles, players, tasks, log tail for one server |
| Console | `2` | Classified log lines plus a command input |
| Players | `3` | Live sessions, seven days of history, occupancy by hour |
| Mods | `4` | Load order, versions, updates, conflicts |
| Settings | `5` | A form rendered from the game's schema, with an apply diff |
| Tasks | `6` | The running task expanded to its steps, plus schedule and history |
| Backups | `7` | Snapshots, sizes, restore |
| New server | `n` | The provisioner wizard |

Plus **ambient mode** (`F`): drops the rail, dims the chrome, enlarges to a
card per server, slows refresh to 5s. Any key exits.

Two details worth keeping. The dashboard's fourth tile is **game-supplied** —
the plugin's `Parse` emits a named metric, so Zomboid shows zombies alive and
Valheim shows world save duration with no per-game UI code. And repeated
identical log lines collapse into a `3x` counter, because a crash-looping mod
otherwise erases the last hour of history in seconds.

Under 100 columns the rail collapses to a pager in the breadcrumb and
navigation moves entirely to keys and the palette; under 30 rows the tile strip
becomes one line of inline values. Every view has a real narrow layout, not a
clipped wide one.

## 4. Visual system

A TUI has three levers — glyph, colour and position — and only position is
reliable, so **state is always encoded twice** and colour is never the only
carrier of meaning.

| Glyph | State | Colour | Meaning |
| --- | --- | --- | --- |
| `●` | running | green | Up and healthcheck passing |
| `◐` | transitioning | amber | Starting, stopping, updating |
| `○` | stopped | grey | Deliberately down; exit code shown |
| `✕` | crashed | red | Exited non-zero or restart-looping, with a reason |
| `!` | degraded | amber | Up but unhealthy, or a mod update pending |
| `?` | unknown | magenta | Docker unreachable — an honest state, not "stopped" |

Amber is the interface accent: focus, selection, the current sample on a
sparkline. It is never a status. Statuses own green, red and blue. Magenta is
reserved for chat so a conversation separates instantly from server output, and
cyan for admin commands you issued. Six hues, each with one job.

**Degradation.** `GARRISON_COLOR=truecolor|256|16|none` overrides profile
detection, which is wrong often enough on Windows to matter; at `none`, every
status falls back to glyph plus word, which is why both always render. A
`--ascii` flag replaces box drawing and block elements entirely.

## 5. Architecture

Five layers, strictly one-directional. Commands go down as `tea.Cmd`; state
comes back up as `tea.Msg`. The TUI never calls Docker, never blocks, and never
holds a lock.

```
model              leaf — shared types, imports nothing of ours
  ^
games   host       describe vs. execute; neither imports the other
  ^       ^
tasks   services
  ^
core               the store: one writer, immutable snapshots
  ^
tui  ·  cmd        two peers, both consumers of core
```

**Why one writer.** Four goroutine families produce state concurrently: stats
streams (one per container), log streams (one per container), task steps, and
pollers. If they all wrote shared structs, the project would be a hunt for
races that surface as a garbled frame at 2am. Instead each sends a typed
mutation on one channel; one goroutine applies them and publishes an immutable
`Snapshot`. The TUI's `Update` does nothing but render. Exactly one mutex
exists, inside the store, and it is uncontended.

```
cmd/garrison/main.go
internal/
  model/      instance plan file event player       leaf; everyone imports it
  tui/        app keys theme layout view
    views/    fleet dashboard console players mods settings tasks backups wizard
    comp/     rail statusbar sparkline table form modal palette toast confirm
  core/       store mutations snapshot actions
  host/       driver plan                           named host/, not runtime/
    docker/   driver stats logs exec events
  games/      game registry schema caps conn
    zomboid/ valheim/ palworld/
    all/                                            one blank import per game
  tasks/      engine lane kinds schedule persist
  services/   metrics logs mods rcon backup alerts
  store/      sqlite migrations
  arch/       the dependency rule, as a test
```

**Dependencies, kept short.** `bubbletea`, `bubbles`, `lipgloss` for the UI;
`docker/docker/client` for the runtime; `modernc.org/sqlite` (pure Go, so no
cgo and no MSVC toolchain on Windows); `go-runewidth`; a small RCON client; and
`robfig/cron/v3`. Everything else is stdlib.

## 6. The game interface

Four required methods plus `Parse`; everything else is an optional capability
discovered by type assertion. Adding Satisfactory or Enshrouded should be an
afternoon of declarations, not a week of rebuilding a dashboard.

```go
type Game interface {
    Meta() Meta                                        // identity and defaults
    Plan(inst model.Instance) (model.Plan, error)      // the container it wants
    Schema() Schema                                    // typed settings
    Compile(inst model.Instance) ([]model.File, error) // settings -> config files
    Parse(line string) model.Event                     // one line -> typed event
}
```

Optional capabilities live in `internal/games/caps.go`: `Rostered`,
`Commandable`, `Drainable`, `Moddable`, `Backupable`, `Probeable`. A game
implementing none still gets a dashboard, console, settings, tasks and backups
— it just gets a Players view that explains itself instead of an empty table.

**Why "plugin" means compile-time.** Go itself is fully supported on Windows;
`go build` produces a native `.exe` with no runtime and no container. The one
narrow thing that does not work there is Go's `plugin` package, which loads
shared objects at runtime and is Linux, macOS and FreeBSD only. So a plugin
here is a Go package compiled in and found through a registry — better anyway:
type safety, one file to ship, no ABI to version, rebuilds under a second. If
third-party game definitions ever matter, the escape hatch is out-of-process
providers over gRPC (`hashicorp/go-plugin`), which does work on Windows; every
method above takes and returns plain data so it could be served that way. Do
not build that until someone other than you wants to add a game.

### Three games, one interface

The point is the ragged right-hand side. These games agree on almost nothing,
and the UI does not care.

| | Project Zomboid | Valheim | Palworld |
| --- | --- | --- | --- |
| Steam app id | 380870 | 896660 | 2394010 |
| Config surface | `servertest.ini` + `_SandboxVars.lua` (Lua table) | env vars + `adminlist.txt` | `PalWorldSettings.ini`, all options inside one `OptionSettings=(...)` line |
| `Compile` writes | 2 files, 2 syntaxes | 0 files — all env, so changes need a recreate | 1 file, and that parenthesised format is exactly why this is per-game code |
| `Rostered` | yes, RCON `players` | no — derived from `Parse` (handshake, `ZDOID`) | yes, REST `/v1/api/players` |
| `Commandable` | yes, RCON | no — input line explains why | yes, RCON |
| `Drainable` | yes: `servermsg`, `save`, `quit` | partial — no warn channel, so drain reduces to "wait for empty" | yes: `Broadcast`, `Save`, `Shutdown 60` |
| `Moddable` | yes — Workshop, load order matters, IDs go in two config keys | yes — Thunderstore/BepInEx, dropped into a plugins dir | no — view replaced with an explanation |
| 4th tile metric | zombies alive | world save duration | base pals |
| Stop signal | `SIGTERM`, 120s grace | `SIGINT` — the image traps it to save | `SIGTERM`, 60s grace |

Command names, app ids and endpoints are starting assumptions from current
versions. Each plugin's job includes pinning them and shipping log fixtures, so
a game update that changes a log line fails a test instead of silently emptying
the Players view.

## 7. Keeping it maintainable

### The dependency rule, enforced

`internal/arch` parses every non-test source file, builds the transitive
internal dependency graph, and fails on a banned edge. It parses sources rather
than shelling out to `go list` so that `go test`'s cache tracks the files it
read — otherwise it caches a pass and stops re-running exactly when the
packages it checks are the ones that changed. A second test checks the rule
against a synthetic graph, so a green `TestDependencyRule` means the repo is
clean rather than the checker being broken.

### The view contract

Left alone, the app shell grows a `switch` per feature — one for rendering, one
for live keys, one for titles — and every new view means editing all of them.
So views implement one interface and register in a slice, exactly like games:

```go
type View interface {
    ID() ViewID
    Title() string
    Keys() []key.Binding                               // help overlay + hints
    Available(inst model.Instance) (bool, string)      // capability gate
    Update(msg tea.Msg, f Frame, snap core.Snapshot) (View, tea.Cmd)
    Render(f Frame, snap core.Snapshot) string
}
```

`Update` takes the `Frame` as well as the message, which an earlier draft of
this section left out. Without it a view has to stash the selected server
during `Render` to use during `Update`, which works only because Bubble Tea
happens to render before every update — an ordering nothing states and one
refactor could break, leaving a view acting on a stale selection.

The `Frame` also carries the selected server and whether the stage has focus.
Selection belongs to the shell rather than to each view: the rail and the stage
show the same choice, and two cursors that can disagree about which server you
are looking at is a bug waiting for a busy evening.

`Available` is the piece that matters: the Mods view itself returns
`(false, "Palworld has no mod system")`, so no capability knowledge leaks into
the shell. The rail order, the number keys and the help overlay all derive from
the registry slice, so adding a view is a package and a line.

### The seven seams

| To add | You write | You do not touch |
| --- | --- | --- |
| A game | `internal/games/<name>/` + a line in `games/all.go` | any view, the task engine, the settings form, the log pipeline |
| A view | `internal/tui/views/<name>/` + a line in `views/all.go` | the shell, the rail, the keymap overlay, other views |
| A task kind | a `Kind` and a `[]Step` builder | progress, cancellation, persistence, lanes, the Tasks view |
| A setting | one `Field` in that game's `Schema()` | the form, validation, the diff, the confirm modal, restart logic |
| A metric | `Event{Metric, Value}` from that game's `Parse` | the metric rings, the sparkline, the tile, downsampling |
| An alert | a rule in `services/alerts`, or an `Event` of kind `Error` | the alert list, acknowledgement, the status bar |
| A runtime | one `host.Driver` — SSH, Podman, plain processes | everything above `internal/host` |

### What is testable without Docker

Almost all of it; the suite runs with the engine stopped.

- **Plugin functions are pure.** `Parse` against captured log fixtures in
  `testdata/`; `Compile` against a real `servertest.ini` from a working server,
  compared as a golden file; `Schema` for validity — every field's default
  inside its own range, no duplicate keys, every enum option reachable.
- **Store mutations are reducers.** Snapshot in, mutation in, snapshot out. No
  Docker, no clock, no goroutines. Crash-loop detection, session accounting and
  drift detection get tested properly here.
- **Task steps run against a fake driver** that records calls and can be told
  to fail. Worth asserting: steps run in order; failing step 7 runs `Undo` for
  6..1 in reverse; cancelling mid-step leaves recorded state consistent; a busy
  lane queues rather than interleaves.
- **Views get golden renders.** Pin the colour profile
  (`lipgloss.SetColorProfile(termenv.Ascii)`), render at 120x34 and 80x24
  against a fixed snapshot, diff against a `.golden` file. It is the only cheap
  thing that catches "a long server name pushes the task column off screen."
- **One integration test** behind `//go:build integration` creates a real
  container from a tiny image, starts it, reads stats and logs, removes it.

### Smells with mechanical fixes

| If you write | It means | Do this |
| --- | --- | --- |
| `switch inst.Game { case "zomboid":` outside `internal/games` | a missing capability interface | add it to `caps.go`, assert at the call site — the most important one |
| a view importing `internal/host` | a missing snapshot field or action | add it to the store |
| a `Step` with nil `Undo` that changes a volume | an undeclared risk | declare the compensation, or fold into a neighbour |
| two views drawing the same table differently | a missing component | promote to `tui/comp` |
| `inst.Settings["MaxPlayers"]` by string in the TUI | bypassing the schema | go through `Schema()` |
| a `sync.Mutex` outside `internal/core` | concurrency leaking upward | send a mutation instead |

**When to split a view:** about 300 lines of `Render`, or the moment it has two
independent cursors — that is two views wearing one hat, and Settings (group
list plus field list) gets there first. The fix is a sub-pane component with
its own focus, not a bigger `Update` with a mode flag.

## 8. Data pipelines

### Logs

One `docker logs --follow` per running container, demultiplexed. Each line goes
through the plugin's `Parse` and becomes a typed `Event`, which one fan-out
goroutine distributes:

```
docker logs --follow
  -> line channel (cap 4096, drop oldest on overflow)
  -> game.Parse(line) -> model.Event
  -> fan-out (one goroutine, no locks):
       console ring buffer      16k lines per server
       player table reducer     join/leave/death -> the roster
       metric extractor         Event.Metric + Value -> 4th tile
       alert matcher            Error kinds, crash loops, your own rules
       event log -> SQLite      chat, joins, admin, deaths; kept 90 days
```

The UI reads snapshots on its render tick and never reads the channel, so a log
flood slows nothing and drops the oldest lines rather than the newest. Only
chat, sessions, admin actions and deaths are persisted — writing every `INFO`
line to SQLite would grow gigabytes a week and answer no question you will ask.

### Metrics

Docker's stats endpoint with `stream=1` pushes roughly one sample per second.
CPU percentage must be computed from deltas:
`(cpu_delta / system_delta) * cores * 100`. Memory needs care: under the WSL2
backend `usage` includes page cache, so the honest figure is
`usage - stats.inactive_file` (cgroup v2). Report the wrong one and every
server looks like it is about to be OOM-killed.

| Tier | Resolution | Span | Where | Feeds |
| --- | --- | --- | --- | --- |
| hot | 1s | 5 min, 300 pts | memory ring | dashboard sparklines, the live number |
| warm | 10s | 1 h, 360 pts | memory ring | fleet strip, "was it spiking an hour ago" |
| cold | 60s | 30 d | SQLite | occupancy, capacity decisions, survives restarts |

Downsampling happens on rollover in the receiving goroutine: a full hot ring
flushes its mean, min and max into one warm point. About 60 KB resident per
server for six series.

### Polls

Everything not a stream is a ticker with a deliberately chosen interval,
because "refresh everything every second" is how a background TUI ends up
burning a core forever. Roster every 10s (1s while Players is focused),
container inspect 5s, mod checks hourly, disk free 60s, host stats 2s. Ambient
mode multiplies every interval by 2.5. All stop while frozen with `Space`.

## 9. Task engine

Everything slower than a frame is a task: a named sequence of steps with a
durable record, a declared compensation, and a lane. Nothing does slow work
outside this engine, so there is one place that knows how to show progress,
cancel safely, and survive a crash.

```go
type Task struct {
    ID      string
    Server  string
    Kind    Kind      // Create Start Stop Restart Update ModSync ApplyConfig Backup Restore
    Steps   []Step
    Cursor  int       // running step; persisted before each one
    State   State     // Queued Running Paused Done Failed Canceled RolledBack
    Trigger Trigger   // Manual Scheduled Chained
}

type Step struct {
    Name string
    Run  func(context.Context, *StepCtx) error
    Undo func(context.Context, *StepCtx) error // best-effort, reverse order
    Est  time.Duration                         // median from history
}
```

**Lanes.** One per server, serialised — two tasks must never touch the same
data volume, because a backup running while an update rewrites the world
directory produces a corrupt archive you discover when you need it. Lanes run
in parallel across servers. A task against a busy lane queues and says why:
"waiting — update to 0.6.2 is on step 5/9."

**The flagship task.** Restart-with-drain is the composition everything else is
a subset of:

```
warn (15m, 5m, 1m) -> save -> stop (SIGTERM, 120s) -> snapshot volume
  -> update image/mods -> recreate -> start -> healthcheck (90s budget)

on failure or cancel: restore the step-4 snapshot, start the previous image,
alert, and do not retry.
```

The snapshot is the compensation for every later step, which is what makes
cancelling at step 5 and a failed healthcheck at step 8 have the same defined
outcome. Two scheduling policies are the difference between a useful
automation and one you turn off after a week: **skip if occupied** (defer while
anyone is online, retry hourly, give up at a stated hour and say so) and **wait
for empty** (hold the lane, warn at intervals, hard cap). The occupancy chart
in the Players view exists to inform which you pick and at what hour.

**Durability.** The record and cursor are written before each step. On startup,
any task still marked `Running` means the process died mid-step; Garrison does
*not* silently resume it. It marks the task failed with "interrupted at step
N", reconciles actual container state against the plan, and raises an alert.
Automatic resume of a half-finished volume operation is how you lose a world.

## 10. Settings

The settings view has no game-specific code. A plugin returns typed fields; the
form, validation, help, diff, confirm modal and follow-up task all derive from
them.

```go
type Field struct {
    Key      string // the game's own key: "MaxPlayers", "ZombieConfig.Speed"
    Label    string // "Max players"
    Group    string // becomes a group in the settings rail
    Type     Type   // Bool Int Float String Enum Duration List Secret
    Default  any
    Min, Max any
    Options  []Option
    Help     string
    Impact   Impact // Live | Restart | Recreate | WipeRisk
    Advanced bool
}
```

| Impact | UI | On apply |
| --- | --- | --- |
| `Live` | no badge | write the file, push over RCON if the game accepts it at runtime |
| `Restart` | amber badge | offer apply-and-restart with a drain, or write now and restart later |
| `Recreate` | blue badge | destroy and recreate the container from a new `Plan`; volume untouched |
| `WipeRisk` | red, needs a second `Enter` | confirm by typing the server name; a backup is taken first, unconditionally |

Two consequences make this a good trade. The view gets features for every game
at once: search across keys, "show only what differs from default", copy
settings from another server of the same game, export a snippet. And `Compile`
stays a pure function from settings to file contents, so it is trivially
testable against a real config file captured from a working server, and the
apply diff is *generated* rather than described.

## 11. On disk

Config is human-readable TOML you can edit with the tool closed.

```
%APPDATA%\Garrison\
  garrison.toml            docker endpoint, data root, theme, intervals
  garrison.db              SQLite: tasks, sessions, events, cold metrics, backups
  servers\<name>.toml      one file per server: the whole state of it
  logs\garrison.log        Garrison's own log, rotated at 10 MiB

D:\gameservers\<name>\
  data\                    bind-mounted into the container
  backups\                 2026-09-09T0220.tar.zst
```

A server file holds `game`, `image`, `data`, `[resources]`, `[[ports]]`,
`[settings]` (keys are the game's own, validated against `Schema()`), `[[mods]]`
in load order with optional `pin`, and `[[schedule]]` entries.

**Reconciliation, not ownership.** Containers carry `garrison.managed=1`,
`garrison.instance`, `garrison.game` and `garrison.plan=<hash>`. On startup
Garrison lists labelled containers and compares each plan hash against what its
TOML currently produces. A mismatch is reported as "config has drifted —
recreate to apply" rather than silently corrected, because the drift is often
something you did by hand for a reason. Losing `%APPDATA%` is recoverable: the
labels plus the volumes are enough to regenerate the server files.

**Secrets** do not go in the TOML. Server, RCON and admin passwords live in
Windows Credential Manager under `garrison/<instance>/<key>`; the TOML holds a
reference. That costs one dependency and makes the servers directory safe to
sync or commit.

## 12. Build order

Each milestone ends with something usable, ordered so the hard architectural
decisions are forced while the code is still small enough to change.

- **M0 — driver and fleet list.** `host.Driver` with a Docker implementation,
  the store with its single writer, a Fleet view that lists labelled containers
  and can start and stop them. One hardcoded server, no registry yet. Proves
  the named pipe and the render loop on real hardware. Do not build anything
  else until it runs for an hour without a glitch.
- **M1 — live truth.** Stats into the three-tier rings, logs into the console
  ring, the Dashboard with sparklines. Extract the `Game` interface (`Meta`,
  `Plan`, `Parse`) and move Valheim behind it.
- **M2 — the task engine.** Lanes, steps, compensation, persistence, the Tasks
  view. `Restart` and `Backup` first, then `Update`. Scheduler last: a task
  engine you cannot watch is not one you should automate.
- **M3 — second game, the real test.** Zomboid: two config files in two
  syntaxes, RCON, Workshop mods with load order. Everything the interface got
  wrong shows up here, which is the point of doing it at M3 rather than M6.
  Ship `Schema`, the settings form and the apply diff in this milestone.
- **M4 — players and mods.** `Rostered` and `Moddable`, session history,
  occupancy, the Mods view with reordering and update checks. Palworld as the
  third game, to confirm a game with no mods degrades cleanly.
- **M5 — provisioning and polish.** The wizard, port scanning, restore, ambient
  mode, the command palette, CLI subcommands, and the 80-column layouts.

Valheim goes first deliberately: simplest plugin, and no RCON at all, which
forces log-derived player state early rather than letting the design assume
RCON exists.

## 13. Risks and open questions

**Things that will bite.**

- **Log formats change with game updates.** The most likely source of silent
  breakage: a patch renames a handshake line and the Players view quietly
  empties. Mitigation is structural — each plugin ships captured fixtures in
  `testdata/` and a table test over `Parse`, so an update fails CI rather than a
  Tuesday evening. Budget for re-capturing fixtures a few times a year.
- **Bind-mount performance on Windows.** Bind mounts from `D:\` into a Linux
  container cross the WSL2 filesystem boundary and are slow, noticeably so for
  Zomboid, which writes many small chunk files. If saves stutter, move the data
  root inside the WSL2 filesystem or use a named volume, and accept that backups
  then go through `docker cp` or a helper container. Decide at M0 by measuring,
  not at M4 by suffering.
- **Steam Workshop resolution.** Getting a mod's current version and
  dependencies needs a Steam Web API key or page scraping; both are fragile.
  Keep it behind one resolver so it can be swapped. Mod *installation* is done
  by SteamCMD inside the container — Garrison never downloads mods itself.
- **Memory over weeks.** Every buffer here is bounded on purpose. The failure
  mode to watch is a stats or log stream that reconnects without the old
  goroutine exiting. Worth writing early: run against four containers for 24
  hours and diff `runtime.NumGoroutine()`.
- **Crash-loop amplification.** A container with `restart: unless-stopped` that
  crashes on boot produces a log flood, a stats stream reconnecting every few
  seconds, and an alert per cycle. Detect three restarts in ten minutes, stop
  reconnecting, raise one alert, and let the operator decide.

**Open questions.**

- **How complete does the CLI need to be?** Full parity doubles the surface to
  maintain. Suggested answer: only `start`, `stop`, `restart`, `backup`,
  `update`, `status` — what a scheduled job or a stream-deck button wants.
- **Notifications outside the terminal.** A Discord webhook on crash, backup
  failure and update completion is maybe 40 lines and is probably the feature
  you will want most once the tool works. Deliberately not in M0–M5; add it once
  the alert matcher has a stable event shape.
- **Remote hosts.** `host.Driver` is already the seam, and pointing it at a
  remote endpoint would mostly work — but log streaming and volume backups over
  a network need different assumptions. Do not design for it now; just do not
  close the door.
- **Shared Workshop cache.** Two Zomboid servers download the same 38 mods
  twice. A shared read-only mod volume fixes it with a small change to `Plan`,
  but complicates per-server pinning. Defer until you actually keep two.
