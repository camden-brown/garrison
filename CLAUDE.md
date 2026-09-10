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

**M0, M1 and M2 are built, and M3's settings form with them.**
`go run ./cmd/garrison` opens a Fleet view, a Dashboard, a Settings form
and a Tasks view over real containers; `garrison status` prints a one-line
summary for a scheduled job.

- `internal/host/docker` — the driver. Endpoint resolution handles npipe,
  unix sockets and bare paths; stats compute CPU from deltas and subtract the
  page cache; logs are demultiplexed unless the container has a TTY.
- `internal/host/fake` — the fake driver. Everything above `host` is tested
  against it, which is why the suite passes with Docker stopped.
- `internal/core` — the store. Typed mutations, one writer, immutable
  snapshots, conflating subscriptions.
- `internal/services/fleet` — the poller and the start/stop controller. The
  only place M0 does I/O.
- `internal/services/streams` — one goroutine per live container, shared by
  the stats and log streamers. The leak accounting exists once and is tested
  there.
- `internal/services/metrics` — the stats streamer. `model.History` holds the
  hot (1s/5min) and warm (10s/1h) tiers; cold points come out of `Add` for
  M2's SQLite and nothing takes them yet.
- `internal/services/logs` — one `docker logs --follow` per container, each
  line through the game's `Parse`, batched to the store every 100ms so a log
  flood cannot drive the render loop.
- `internal/games/valheim` — the first plugin. Fixtures in `testdata/` are a
  captured session from a real server, not documentation.
- `internal/games/zomboid` — the second, and the one M3 was for. 144 ini keys
  and 269 sandbox variables in `keys_gen.go`, generated from fixtures
  extracted out of a real install; `Compile` writes both files whole and
  `Plan`'s env mirrors the thirteen keys the image rewrites on every boot
  ([ADR 0011](docs/decisions/0011-compile-writes-whole-files.md)). It is the
  first game to implement any of `caps.go`: `Rostered`, `Commandable`,
  `Drainable` and `Moddable`.

  Its player events never reach stdout — the server writes joins, leaves and
  chat to files inside the volume — so its roster comes over RCON. That is the
  cleanest vindication of the capability split so far: the Players view works
  the same over a log-derived roster and an asked-for one, and neither it nor
  the store knows which it has.
- `internal/services/conn` — the RCON transport, implementing `games.Conn`.
  Source RCON over TCP, one serialised connection per server, reconnect and
  retry once because the usual failure is a socket the server closed while
  nothing was using it. `Pool` keys connections by container so a recreated
  one gets a fresh socket rather than a port that has moved.
- `internal/services/command` — console commands. **Not a task, on purpose**:
  a command is a question, its answer belongs in the console, and a task lane
  entry beside every chat message would make the Tasks view useless. The
  reply arrives as a console event, where it would have appeared anyway had
  the server said it unprompted.
- `internal/services/players` also polls rosters for games that implement
  `games.Rostered`, so an asked-for roster replaces an inferred one and both
  land in the same snapshot field.
- `internal/services/mods` — resolves configured mod ids into names, sizes and
  update badges against the Steam Workshop, hourly. One request for a whole
  server's list rather than one per mod, and the configured order is
  preserved: for a game where load order matters, that order is the
  operator's decision and a resolver sorting it would reorder their server.

**Capabilities reach a plugin pre-bound.** `internal/tasks` and the services
must not import `internal/games` — `internal/store` depends on tasks and the
dependency rule forbids anything under store reaching games — so `cmd` asserts
`games.Drainable`, `Commandable` and `Rostered` and adapts each to a narrow
interface the consumer declares. The arch test found that, which is exactly
what it is for.
- `internal/tasks` — the engine. Lanes are per server and serialised; steps
  declare compensation; the journal is written before every step and an
  interrupted task is failed and named rather than resumed.
- `internal/store` — SQLite. The task journal, the cold metric tier and the
  events worth keeping, with append-only migrations.
- `internal/config` — one TOML file per server, atomic saves, hand-editable.
  `Delete` removes a server's file and never its data directory; `tasks.Delete`
  writes the file back if the container removal fails, so a half-done delete
  leaves a server Garrison still knows about rather than an orphan.
- `internal/services/backup` — tar+zstd beside the world it came from, with
  a restore that refuses to write outside its destination.
- `internal/services/scheduler` — cron, with the two policies that decide
  what a due job does when people are playing.
- `internal/services/backup` also polls the archive directories, so the
  Backups screen lists what is on disk rather than only what Garrison made.
- `internal/services/players` — diffs the roster on a timer and writes
  sessions to SQLite, which is what the Players view's occupancy chart is
  computed from. It diffs a roster rather than reading join/leave events, so a
  game that answers over RCON later changes nothing here.
- `internal/tui` — the shell, the rail, the theme, the `View` contract, and
  `comp` (panel, tile, sparkline, text, input, filter, log). All eight views
  are built: fleet, dashboard, console, players, mods, settings, tasks,
  backups. The shell also owns the things that are not screens — the ":"
  command line, ctrl+P palette, "n" provisioner, "X" delete, "[" and "]" to
  step between servers, "F" ambient mode, "y" share and the "?" help overlay,
  which is generated from the View contract's Keys rather than written out.

  **The three typed confirmations are all built and there are only three**, as
  DESIGN requires: deleting a server, restoring over a live world, and
  applying a setting the game marks ImpactWipeRisk. Each asks for the server's
  name in full. Note that they cancel on esc alone and never on "n" — a server
  called valheim-main cannot be typed out if the n in it dismisses the prompt.

  Sharing copies over **OSC 52** (`termenv.Copy`, already a direct dependency)
  and shows the same text on screen, because a terminal is free to ignore the
  escape and a copy whose only evidence is somebody else's paste fails
  silently. The join address comes from `Instance.Address` — Garrison cannot
  derive it, since the DNS name and the router forward are both outside what
  it can see.
  `comp.Input` is the only text editor: the value it edits lives in the store
  and only the caret is the view's, so a resize cannot lose what was typed.

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

Debts still outstanding, all deliberate and all noted in the code:

1. The roster binds a name to a connection by claiming the oldest unnamed
   one, because Valheim's log never links the two. It mis-pairs two players
   who finish loading in a different order than they connected. **This is
   now Valheim's problem alone**: Zomboid implements `games.Rostered` and the
   roster poller asks it instead, so the inference runs only for games that
   cannot be asked.
3. `model.Mount` cannot express a Docker named volume — only a host path. The
   measurement in "Platform facts" says a bind mount from a Windows drive is
   ~32× slower than a volume for small-file writes, so this is now a number
   rather than a suspicion, and it is the strongest argument for giving Mount
   a volume form.
4. Restore and delete have no *scheduled* form, deliberately. Both are things
   a person asked for by typing a server's name; there is no policy that would
   run either unattended and none worth inventing.
5. Valheim has no mod management, and cannot with this interface. `Moddable`
   is shaped around mods a server downloads from its own config — Zomboid's
   Workshop ids, which is why reordering and update checks work there — and
   Valheim's BepInEx plugins are files somebody drops in a directory. Whether
   the interface needs an install path is a question for the first game that
   actually needs one; guessing now is what ADR 0006 warns against.
5. Drain works for games with a channel and degrades for those without.
   `RestartWithDrain` warns at 15m, 5m and 1m, saves, then stops; a game with
   no `games.Drainable` skips the wait and says so in the task's history
   rather than delaying a restart that helps nobody. Valheim is still the
   game with no way to warn anyone.

Closed since M1: start/stop are tasks, the stop record is durable in SQLite,
and the cold metric tier has somewhere to live. Closed since M2: the console
is a real 16k ring ([ADR 0010](docs/decisions/0010-the-console-ring-shares-its-storage.md))
with a Console screen over it, the settings form can edit text, and backups
have a screen with a restore behind a typed confirmation. Closed since then:
Players and Mods, the "/" filter, the ":" command line, the ctrl+P palette,
ambient mode, the provisioner, delete, the server steppers, the generated
apply diff, and start/stop/restart/backup/update as subcommands that wait for
their task and exit on its result.

**M5 is done and M3/M4 are held open by one thing:** a second game. The
console's command input, drain and the roster's name pairing are the same
missing capability three times over, not three jobs.

**Restore is the one destructive path that undoes itself.** `tasks.Restore`
stops the server, archives the world it is about to replace, unpacks the chosen
archive, and starts again — and that safety archive is the declared
compensation for the unpack, so a truncated archive or a server that will not
come back leaves the previous world in place. The typed prompt in the view
guards against meaning the wrong thing; the task guards against everything
else.

`fleet.DefaultStopGrace` is 60s and nothing overrides it yet, so stopping a
container that ignores SIGTERM takes a full minute before the kill. From M1
the signal and grace come from the game's `model.Plan`, which is where they
belong — Valheim traps SIGINT, Zomboid needs 120s.

Verified end to end against real Docker: fleet by label, live CPU/memory/network
histories, Valheim's log parsed into a roster and a `world save = 314` metric
tile, with no game-specific code in any view.

**Two games, and no third planned.** Palworld was on the roadmap at M4 and has
been dropped; adding a game is a package and a line in `games/all`, so another
one is work to be done rather than a milestone to be reached.

**Every milestone is done.** What remains is the debt list above, and none of
it is a milestone: two of the six are facts about Valheim rather than gaps in
Garrison, one is a measurement arguing for a `model.Mount` change, and the
render loop needs an hour on Windows hardware that no test here can supply.

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
- **Bind mounts from a Windows drive cross the WSL2 filesystem boundary and
  are slow. Measured 2026-09-09**, Docker Desktop 29.7.2, `C:` vs. a Docker
  named volume, three runs, inside the container:

  | | bind mount from `C:` | named volume | ratio |
  | --- | ---: | ---: | ---: |
  | 256 MiB sequential write | ~1.90 s | ~0.50 s | **3.8×** |
  | 2000 small files | ~4.9 s | ~0.15 s | **~32×** |

  Sequential throughput is merely poor; the small-file case is the one that
  matters, because that is the shape of a chunked world save. A game that
  writes its save as many small files across a bind mount pays thirty times
  over. `model.Mount` speaks host paths only, so a named volume is not
  currently expressible — that is the gap this number argues for closing.
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
