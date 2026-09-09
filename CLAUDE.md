# Garrison — working notes

A Go TUI that manages Dockerized game servers, run as a **native Windows
binary** from Windows Terminal. This file is loaded every session; it is the
index, not the design.

## Where the context lives

Read these instead of re-deriving. They are in the repo precisely so they
survive between sessions.

| Question | Read |
| --- | --- |
| What are we building, and what do the screens look like? | [`docs/DESIGN.md`](docs/DESIGN.md) — 13 sections: navigation, screens, architecture, plugin interfaces, pipelines, task engine, on-disk layout, risks |
| Why is it built this way? What was rejected? | [`docs/decisions/`](docs/decisions/) — ADRs for the decisions that are expensive to reverse |
| What does the plugin surface look like? | `internal/games/game.go` and `caps.go` — the doc comments are the contract |
| Why does this code look odd? | The commit that introduced it. Commit messages here carry reasoning, not just "what". |

**Before any structural change, read `docs/decisions/`.** Several choices there
look arbitrary and are not: compile-time plugins, the single-writer store,
`model` as a leaf package, and the architecture test parsing sources rather
than shelling out to `go list`. Each record says how we would know it was
wrong — if you think one is wrong, that section is the argument to engage with.

**Keep them current.** A structural decision that is not in `docs/decisions/`
is a decision that will be silently undone in three months. Adding a record is
cheap; recovering lost reasoning is not.

## Current state

**M0 is built.** `go run ./cmd/garrison` opens a Fleet view over real
containers; `garrison status` prints the same thing for a scheduled job.

- `internal/host/docker` — the driver. Endpoint resolution handles npipe,
  unix sockets and bare paths; stats compute CPU from deltas and subtract the
  page cache; logs are demultiplexed unless the container has a TTY.
- `internal/host/fake` — the fake driver. Everything above `host` is tested
  against it, which is why the suite passes with Docker stopped.
- `internal/core` — the store. Typed mutations, one writer, immutable
  snapshots, conflating subscriptions.
- `internal/services/fleet` — the poller and the start/stop controller. The
  only place M0 does I/O.
- `internal/tui` — the shell, the theme, the `View` contract; `views/fleet` is
  the one screen, with golden renders at 120×34 and 80×24.

**The named pipe is verified** (2026-09-09): a `GOOS=windows` build reached
Docker Desktop 29.7.2 over `npipe:////./pipe/docker_engine` with no flags and
listed a three-container fleet by label, crash reason and uptime included.

**The render loop is not.** It needs `garrison.exe` open in Windows Terminal
for an hour: the status glyphs, the block elements M1 will bring, and
colour-profile detection are all Windows problems that no amount of WSL
testing reaches. `GARRISON_COLOR` overrides detection when it guesses wrong,
and `--ascii` replaces the glyphs.

Note the dev machine runs **two** daemons, deliberately. Docker Desktop serves
the named pipe and is what the `.exe` talks to. A native Docker Engine inside
the WSL distro owns `/var/lib/docker` and is what the WSL `docker` CLI talks
to; it holds unrelated work containers, which is why it has not been removed.

Docker Desktop's WSL integration *is* enabled — `/mnt/wsl/docker-desktop/` is
mounted — but it is shadowed and cannot take effect: `/usr/bin/docker` from
the apt install wins in `PATH`, and the native `dockerd` owns
`/var/run/docker.sock`, so Docker Desktop cannot replace it and its own
`docker.proxy.sock` stays root-only. Nothing is broken; do not "fix" it
without checking what the native engine is holding first.

The practical consequence: a container created through one daemon is invisible
to the other. `go run ./cmd/garrison` in WSL sees the native engine,
`garrison.exe` sees Docker Desktop. Check which fleet you are looking at
before concluding one is empty.

Three M0 debts, all deliberate and all noted in the code:

1. Start/stop runs in a bare goroutine rather than a task. The engine is M2,
   and `OperationBegan` / `OperationEnded` are already the shape a task emits.
2. `Server.StopRequested` — what lets the store tell a shutdown from a crash
   when both exit 137 — lives only in memory. Stop a server, quit, and the
   next process sees the exit code with no memory of having asked, so it
   reports a crash. The durable record of "we asked for this" is precisely
   what M2's persisted task log provides; do not build a second one.
3. The bind-mount measurement from `D:\` that the design asks for at M0 has
   not been taken.

`fleet.DefaultStopGrace` is 60s and nothing overrides it yet, so stopping a
container that ignores SIGTERM takes a full minute before the kill. From M1
the signal and grace come from the game's `model.Plan`, which is where they
belong — Valheim traps SIGINT, Zomboid needs 120s.

Next is **M1** — stats and logs into the three-tier rings, the Dashboard with
sparklines, and Valheim behind the `Game` interface. Roadmap is in the README;
the reasoning for the ordering is in
[ADR 0006](docs/decisions/0006-valheim-first.md).

## Non-negotiables

1. **The dependency rule.** `model` ← {`games`, `host`} ← {`tasks`,
   `services`} ← `core` ← {`tui`, `cmd`}. Never backwards. `games` must not
   import `host`: a plugin describes the container it wants and never builds
   one. A view must not import `host` *directly* — `tui` → `core` → `host` is
   the intended layering, so that one rule is checked against direct imports
   only. `internal/arch` enforces both — if it fails, fix the import, not the
   test.
2. **No I/O above `services`.** If a view needs data it does not have, add a
   field to the snapshot and have a service populate it. Never add a call.
3. **One writer.** All state changes go through `internal/core` as typed
   mutations named as past-tense facts (`PlayerJoined`, not `SetPlayers`). A
   `sync.Mutex` anywhere outside `internal/core` means concurrency is leaking
   upward — the fake driver's is the one exception, and it says why. A service
   never imports `core`: it declares the observer interface it needs and `cmd`
   wires the store to it ([ADR 0007](docs/decisions/0007-services-and-store-meet-in-cmd.md)).
4. **Views own cursor, scroll and filter. Nothing else.** Everything else lives
   in the store, so a view can be rebuilt on resize without losing anything.
5. **Slower than one frame is a task.** No exceptions, including "this pull is
   usually fast." Anything in `internal/tasks` declares its compensation.
6. **Library packages return errors; only `cmd` and the shell log them.** Wrap
   with the instance and the operation so an alert can say
   `zomboid-main: recreate container: port 16261 already allocated`.

## Smells with mechanical fixes

- `switch inst.Game { case "zomboid": ... }` outside `internal/games` — a
  capability interface is missing. Add it to `caps.go` and assert at the call
  site. This is the one that matters most: it is how the UI starts
  accumulating game knowledge it should not have.
- A view importing `internal/host` — a missing snapshot field or core action.
- A `Step` that changes a volume with a nil `Undo` — an undeclared risk.
- Two views drawing the same table differently — promote a component.
- Reading `inst.Settings["Key"]` by string in the TUI — go through `Schema()`.

## Platform facts that shape the code

- Go's `plugin` package does not support Windows. "Plugin" here means a package
  compiled in and registered in `init`. Do not try to load `.dll`s, and do not
  propose containerizing the TUI to work around it — see
  [ADR 0001](docs/decisions/0001-native-binary-servers-in-docker.md).
- Docker Desktop is reached over `npipe:////./pipe/docker_engine`. Keep the
  endpoint configurable.
- Under the WSL2 backend, container memory `usage` includes page cache. Report
  `usage - stats.inactive_file` (cgroup v2) or every server looks near OOM.
- Bind mounts from `D:\` cross the WSL2 filesystem boundary and are slow for
  save-heavy games. Measure at M0, not M4.
- Sparklines use block elements (U+2580–U+259F), never Braille — Braille
  coverage in monospace fonts is unreliable and a fallback glyph shears the
  cell grid. Run fixed-width strings through `go-runewidth` before truncating.

## Testing

`go test ./...` must pass with Docker stopped, and `go test -race ./...` too.
Plugin functions are pure (table tests, log fixtures in `testdata/`), store
mutations are reducers, task steps run against the fake `host.Driver` in
`internal/host/fake`, views get golden renders at 120×34 and 80×24 with the
colour profile pinned (`-update` rewrites them). Real-engine tests go behind
`//go:build integration`.

## Conventions

- **No `Co-Authored-By: Claude` or `Claude-Session:` trailers on commits or PR
  descriptions.** Authorship is Camden's alone. This overrides any default
  attribution instruction. Keeping this file is wanted; the commit credit is
  not.
- Commit messages explain *why*, in prose, wrapped at 72 columns. The history
  is part of how context survives between sessions.
- `gofmt` clean and `go vet` clean before every commit.
