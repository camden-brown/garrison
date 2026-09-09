# 0003 — `model` is a leaf package; `games` and `host` never import each other

**Status:** Accepted · 2026-09-09

## Context

An earlier draft had the game interface return a `host.Plan`:

```go
Plan(inst Instance) (host.Plan, error)   // wrong
```

That makes every game plugin import the Docker layer. Plugins would then be
untestable without the runtime, and a change to the driver would ripple into
every game.

## Decision

Shared types — `Instance`, `Plan`, `File`, `Event`, `Player`, `Health` — live
in `internal/model`, which imports no other Garrison package. `games` and
`host` both import `model` and neither imports the other.

```go
Plan(inst model.Instance) (model.Plan, error)   // right
```

## Consequences

- A game *describes* the container it wants; `host` is the only thing that
  builds one. The two can be developed and tested independently.
- `model.Plan` is a plain value, so it can be hashed (drift detection via the
  `garrison.plan` label), diffed, and printed in the wizard's review step
  before anything is created.
- `model` must stay dumb. Behaviour that needs another package does not belong
  in it.

## Rejected

**Types defined in `games`, with `host` importing `games`.** Inverts the
dependency the wrong way: the executor would depend on the describers, so
adding a game would touch the runtime.

**Types defined in `core`.** Everything would import the store, which is the
one package most likely to change.

## How we would know this was wrong

If `model` starts needing imports from other Garrison packages to express a
type, the split is in the wrong place. `internal/arch` fails immediately if
that happens, which is the point.
