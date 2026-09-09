# 0007 — Services and the store meet through interfaces, wired in `cmd`

**Status:** Accepted · 2026-09-09

## Context

[0004](0004-single-writer-store.md) says every producer of state sends a typed
mutation to one writer in `internal/core`. M0 produced the first real producer
— the fleet poller — and with it the question that decision left open: how does
a poller in `internal/services` reach the store, when the dependency rule says
`services` must never import `core`?

Three answers were on the table, and the wrong two are both comfortable.

The first is to let `core` import `services` and hold the channel. The
dependency rule permits it — `core` sits above `services` — and it is the
smallest diff. But it makes the store know every service by name. At M1 that is
logs and metrics; by M4 the store imports half the program, and the package
most likely to change is coupled to all of it.

The second is to move the poller into `core`. Then there is no seam at all —
and `core` does I/O, which breaks "no I/O above `services`" and makes every
store test need a driver.

## Decision

Each side declares the narrow interface it needs, and neither imports the
other. `cmd/garrison` is the only place that names both.

Producing state — the service declares what it will call:

```go
// internal/services/fleet
type Observer interface {
    FleetObserved(ctx context.Context, at time.Time, containers []host.Container)
    FleetUnobservable(ctx context.Context, at time.Time, err error)
}
```

`*core.Store` satisfies it with two methods that do nothing but send a
mutation. Causing effects — the store declares what it needs done:

```go
// internal/core
type Control interface {
    Start(ctx context.Context, instance, id string) error
    Stop(ctx context.Context, instance, id string) error
}
```

`*fleet.Controller` satisfies that. Both interfaces are declared by the
consumer, which is the Go convention and here also the thing that keeps the
layer boundary real rather than aspirational.

## Consequences

- `internal/arch` proves the seam holds: neither import exists, so neither can
  be added back by accident.
- The store is testable with no runtime — `core.Options{Control: nil}` reports
  "no container runtime attached" — and the poller is testable with no store,
  against a recorder that counts calls.
- Adding the M1 log and metric streams adds two more observer interfaces and
  two more methods on the store. It does not change the store's imports.
- The wiring in `cmd` is explicit and slightly tedious: a driver, a store, a
  controller, a poller, two goroutines. That tedium is the price of the two
  packages not knowing about each other, and it is about twenty lines.
- Observer methods take a `context.Context` so a send cannot outlive shutdown.

## Rejected

**A channel of a shared observation type.** Whichever package defines the type,
the other imports it, and the type grows a field per producer.

**`core` importing `services` directly.** Permitted by the dependency rule and
still wrong: it couples the one package that must stay stable to every package
that will not.

**Callbacks as bare `func` fields.** Equivalent in power, worse to read: an
interface with two named past-tense methods says what the producer learned;
`func(time.Time, []host.Container, error)` says nothing.

## How we would know this was wrong

If a producer needs a reply — not "here is what I saw" but "here is what I saw,
tell me what to do" — the observer shape is wrong for it and it wants a
request/response seam instead. Nothing in M0–M2 looks like that. The other
signal is `cmd/garrison` growing past roughly a screen of wiring, which would
mean the composition itself deserves a package rather than a longer `main`.
