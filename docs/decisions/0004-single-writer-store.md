# 0004 — Exactly one writer, in `internal/core`

**Status:** Accepted · 2026-09-09

## Context

Four goroutine families produce state concurrently: container stats streams
(one per container), log streams (one per container), task steps, and periodic
pollers. The UI renders continuously on top of all of it, for weeks at a time.

## Decision

Every producer sends a typed mutation on one channel. One goroutine applies
them and publishes an immutable `Snapshot`. The TUI receives snapshots as
`tea.Msg` and does nothing but render. There is one mutex in the program, it
lives in `internal/core`, and it is uncontended.

Mutations are named as past-tense facts — `PlayerJoined`, `StatsSampled`,
`TaskStepAdvanced` — never `SetPlayers`. A name describing what happened cannot
be accidentally applied twice with a different meaning.

## Consequences

- No shared mutable state above the store, so no data races to chase. The
  failure mode this avoids is a garbled frame at 2am that reproduces once a
  week.
- Views are pure functions of a snapshot, which is what makes golden render
  tests possible at all.
- Views may own cursor, scroll and filter — nothing else — so a view can be
  rebuilt on resize without losing anything that matters.
- Store mutations are reducers: snapshot in, mutation in, snapshot out. Crash
  loop detection, session accounting and drift detection get tested with no
  Docker, no clock and no goroutines.
- A `sync.Mutex` anywhere outside `internal/core` means concurrency is leaking
  upward. Send a mutation instead.

## Rejected

**Shared structs with fine-grained locks.** Faster to write, and the source of
the exact class of bug this design exists to prevent.

**Channels straight into the Bubble Tea program from every producer.** `Update`
would become the reducer, mixing UI concerns with state transitions and making
the CLI impossible to serve from the same code.

## How we would know this was wrong

If the single writer becomes a throughput bottleneck. Given the actual rates —
a few samples per second per container and a log line burst ceiling — that is
implausible; measure before believing it.
