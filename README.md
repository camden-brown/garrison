# Garrison

A terminal dashboard for running game servers in Docker on Windows. One window
that shows every server you own — Project Zomboid, Valheim, Palworld, and
whatever you install next month — and is also the place you change things.

**Status: skeleton.** The architecture, the plugin interfaces and the
dependency rule are in place and enforced by tests. There is no Docker driver
and no TUI yet. See [Roadmap](#roadmap).

## Why

Standing up a game server is a `docker run` line. The annoying part is
everything after: re-learning each game's config format, re-deriving how to
read its logs, re-writing a restart script, and rebuilding somewhere to see
whether it is healthy — once per game, forever.

Garrison makes that work a plugin. A new game is one Go package that declares
its container plan, its settings, and how to read a line of its log. The
dashboard, console, task engine, settings form, mod manager and backup flow are
written once and work the same for every game.

## What it does

- **Fleet view** — every server, its state, load, who is connected, what tasks
  are running against it. Readable from across the room in ambient mode.
- **Control surface** — change a setting, install a mod, schedule a restart
  that warns players first, take a backup, roll one back.
- **Provisioner** — a wizard that turns "a game plugin plus a name" into a
  running, port-mapped, volume-backed server. Ports are proposed by scanning
  the live fleet, so the second Valheim server does not collide with the first.

## What it is not

- **Not a web panel.** No HTTP server, no auth model, no browser. Remote access
  is SSH to the box and run the binary.
- **Not a Docker replacement.** Garrison never stores state Docker already
  holds. Containers are labelled `garrison.managed=1`, so the fleet is
  rediscoverable by listing containers — delete the config directory and it
  finds its servers again.
- **Not multi-user.** One operator, one machine.

## Build

Go is fully supported on Windows; this builds to a single native `.exe` with no
runtime and no container.

```
go build -o garrison.exe ./cmd/garrison
```

If you would rather not install the Go toolchain, use a container as a *build
step* — which is different from running the tool in one:

```
docker run --rm -v "${PWD}:/src" -w /src \
  -e GOOS=windows -e GOARCH=amd64 \
  golang:1 go build -o garrison.exe ./cmd/garrison
```

The tool itself stays a normal Windows process, so it keeps the clipboard,
Credential Manager, and the ability to still be on screen while the Docker
daemon is restarting.

### Tests

```
go test ./...
```

Everything runs with Docker stopped. Plugin functions are pure, store mutations
are reducers, and task steps run against a fake `host.Driver`. The one test
that needs a real engine sits behind `//go:build integration`.

## Architecture at a glance

Imports go one direction and never back:

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

Four goroutine families produce state concurrently — stats streams, log
streams, task steps, pollers — so none of them write shared structs. Each sends
a typed mutation on one channel; one goroutine applies them and publishes an
immutable snapshot. The TUI receives snapshots as messages and only renders.
There is exactly one mutex in the program and it lives in `internal/core`.

`internal/arch` enforces the dependency rule as a test, so it fails the build
instead of decaying into a comment. The rule that earns its keep is **`games`
must not import `host`**: a plugin describes the container it wants and never
builds one.

### Adding things

| To add | You write | You do not touch |
| --- | --- | --- |
| A game | `internal/games/<name>/` + one line in `games/all.go` | any view, the task engine, the settings form |
| A view | `internal/tui/views/<name>/` + one line in `views/all.go` | the shell, the rail, the keymap overlay |
| A task kind | a `Kind` and a `[]Step` builder | progress, cancellation, persistence, lanes |
| A setting | one `games.Field` in that game's `Schema()` | the form, validation, the diff, the confirm modal |
| A metric | return `model.Event{Metric, Value}` from `Parse` | the metric rings, the sparkline, the tile |
| A runtime | one `host.Driver` implementation | everything above `internal/host` |

`internal/games/caps.go` holds the optional capability interfaces. A game
implementing none of them still gets a dashboard, console, settings, tasks and
backups; it just gets a Players view that explains itself instead of an empty
table. Reaching for a `switch` on `Meta().ID` outside `internal/games` means a
capability interface is missing.

## Roadmap

- **M0** — `host.Driver` with a Docker implementation, the store, and a fleet
  list that can start and stop containers. Proves the named pipe and the render
  loop on real hardware.
- **M1** — stats and log streaming, the dashboard with sparklines. Extract the
  `Game` interface and move Valheim behind it. First milestone worth leaving
  open.
- **M2** — the task engine: lanes, steps, compensation, persistence. Restart
  and backup first, then update. Scheduler last.
- **M3** — Zomboid, which is the real test of the interface: two config files
  in two syntaxes, RCON, Workshop mods with load order. Plus the schema-driven
  settings form and the apply diff.
- **M4** — players and mods, session history, occupancy. Palworld as the third
  game, to confirm that a game with no mods degrades cleanly.
- **M5** — the new-server wizard, restore, ambient mode, command palette, CLI
  subcommands, 80-column layouts.

Valheim goes first deliberately: it is the simplest plugin and it has **no
RCON**, which forces log-derived player state early rather than letting the
design assume RCON exists.

## Documentation

[`docs/DESIGN.md`](docs/DESIGN.md) is the full design — screens, keymap,
architecture, the plugin interfaces, the data pipelines, the task engine, the
on-disk layout, and the risks worth knowing about before starting.

## License

MIT — see [LICENSE](LICENSE).
