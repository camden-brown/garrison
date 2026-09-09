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

Scaffold only: `model`, `games`, `host` interfaces, the registry, and the
architecture test. **No Docker driver and no TUI yet.** `go run ./cmd/garrison`
prints the version and the empty game registry.

Next is **M0** — `host.Driver` with a Docker implementation over the named
pipe, the store with its single writer, and a Fleet view that lists containers
by label and can start and stop them. Roadmap is in the README; the reasoning
for the ordering is in [ADR 0006](docs/decisions/0006-valheim-first.md).

## Non-negotiables

1. **The dependency rule.** `model` ← {`games`, `host`} ← {`tasks`,
   `services`} ← `core` ← {`tui`, `cmd`}. Never backwards. `games` must not
   import `host`: a plugin describes the container it wants and never builds
   one. `internal/arch` enforces this — if it fails, fix the import, not the
   test.
2. **No I/O above `services`.** If a view needs data it does not have, add a
   field to the snapshot and have a service populate it. Never add a call.
3. **One writer.** All state changes go through `internal/core` as typed
   mutations named as past-tense facts (`PlayerJoined`, not `SetPlayers`). A
   `sync.Mutex` anywhere outside `internal/core` means concurrency is leaking
   upward.
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

`go test ./...` must pass with Docker stopped. Plugin functions are pure (table
tests, log fixtures in `testdata/`), store mutations are reducers, task steps
run against a fake `host.Driver`, views get golden renders at 120×34 and 80×24
with the colour profile pinned. Real-engine tests go behind
`//go:build integration`.

## Conventions

- **No `Co-Authored-By: Claude` or `Claude-Session:` trailers on commits or PR
  descriptions.** Authorship is Camden's alone. This overrides any default
  attribution instruction. Keeping this file is wanted; the commit credit is
  not.
- Commit messages explain *why*, in prose, wrapped at 72 columns. The history
  is part of how context survives between sessions.
- `gofmt` clean and `go vet` clean before every commit.
