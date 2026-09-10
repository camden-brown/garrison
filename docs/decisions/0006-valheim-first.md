# 0006 — Valheim is the first game implemented

**Status:** Accepted · 2026-09-09

## Context

The first game shapes the interface, because it is the one the interface is
written against. Zomboid is the author's main server and the obvious candidate.

## Decision

Valheim first, at M1. Zomboid second, at M3.

## Consequences

- Valheim is the simplest plugin: configuration is entirely environment
  variables, so `Compile` returns no files at all, which forces the interface
  to handle that case honestly from day one.
- **Valheim has no RCON.** Player state must be reconstructed from log events
  (handshake and `ZDOID` lines). Doing this first prevents a design that
  assumes RCON exists and then has to be unpicked.
- Zomboid at M3 is the real test — two config files in two syntaxes, RCON,
  Workshop mods with load order that must be reorderable. Expect `Schema` and
  `Compile` to change then. That is the point of it being M3 and not M6.
- ~~Palworld at M4 confirms a game with **no** mod system degrades to an
  explanation rather than an empty view.~~ **Superseded 2026-09-10:** Palworld
  was dropped. Valheim turned out to be the game with no mod system Garrison
  can manage, so it is what confirms that degradation — the consequence held,
  the third game was not needed to demonstrate it.

## Rejected

**Zomboid first**, because it is the server actually in use. It has RCON, mods
and a conventional ini file, so it would have produced an interface quietly
assuming all three — and the second game would have paid for it.

## How we would know this was wrong

If Valheim's env-only configuration makes `Compile` look vestigial and we
design it away, then meet Zomboid at M3 and need it back. Mitigation: the
interface keeps `Compile` even though Valheim returns an empty slice.
