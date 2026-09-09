# Garrison — working notes

A Go TUI that manages Dockerized game servers, run as a native Windows binary
from Windows Terminal. Read `docs/DESIGN.md` before making structural changes.

## Non-negotiables

1. **The dependency rule.** `model` <- {`games`, `host`} <- {`tasks`,
   `services`} <- `core` <- {`tui`, `cmd`}. Never backwards. `games` must not
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
  site. This is the one that matters most.
- A view importing `internal/host` — a missing snapshot field or core action.
- A `Step` that changes a volume with a nil `Undo` — an undeclared risk.
- Two views drawing the same table differently — promote a component.
- Reading `inst.Settings["Key"]` by string in the TUI — go through `Schema()`.

## Platform facts that shape the code

- Go's `plugin` package does not support Windows. "Plugin" here means a package
  compiled in and registered in `init`. Do not try to load `.dll`s.
- Docker Desktop is reached over `npipe:////./pipe/docker_engine`. Keep the
  endpoint configurable.
- Under the WSL2 backend, container memory `usage` includes page cache. Report
  `usage - stats.inactive_file` (cgroup v2) or every server looks near OOM.
- Sparklines use block elements (U+2580–U+259F), never Braille — Braille
  coverage in monospace fonts is unreliable and a fallback glyph shears the
  cell grid. Run fixed-width strings through `go-runewidth` before truncating.

## Testing

`go test ./...` must pass with Docker stopped. Plugin functions are pure (table
tests, log fixtures in `testdata/`), store mutations are reducers, task steps
run against a fake `host.Driver`, views get golden renders at 120x34 and 80x24
with the colour profile pinned. Real-engine tests go behind
`//go:build integration`.
