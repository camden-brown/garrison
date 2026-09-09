# 0002 — Game support is compile-time packages, not runtime plugins

**Status:** Accepted · 2026-09-09

## Context

The whole point of Garrison is that adding a game is cheap. "Plugin" suggests
something loaded at runtime. Go's `plugin` package does exactly that — and does
not support Windows, which is the target platform. That is a hard platform
limitation, not a configuration problem.

## Decision

A game is a Go package compiled into the binary that registers itself in
`init`. `internal/games/all` blank-imports each one; `cmd/garrison` imports
`all`. Adding a game is one directory plus one line.

## Consequences

- Type safety across the `Game` interface, one file to ship, no ABI to version.
- A rebuild is about a second, so the iteration loop is no worse than dynamic
  loading would have been.
- Third parties cannot add a game without recompiling. Acceptable: the author
  is the only person adding games and has the source.

## Rejected

**Go's `plugin` package.** Not available on Windows.

**Out-of-process providers over gRPC** (`hashicorp/go-plugin`), which does work
on Windows. Correct escape hatch if third-party game definitions ever matter,
and the interface is deliberately shaped for it — every method takes and
returns plain data, and `Parse` is the only hot path, which would batch. Not
built, because it solves a distribution problem we do not have.

## How we would know this was wrong

Someone other than the author wants to add a game and cannot build from source.
Then implement the gRPC transport behind the same interface; no call site
changes.
