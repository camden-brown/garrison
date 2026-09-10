# 0011 — Compile writes whole files, and Plan must agree with it

**Status:** Accepted · 2026-09-10

## Context

M3 exists to find out whether `Schema` and `Compile` were the right shape, by
implementing a game that uses them properly. Valheim compiles to no files at
all, so until now `Compile` has been satisfied by `return nil, nil` and has
never had to be right about anything.

Project Zomboid writes **144 keys into `servertest.ini` and 269 into a nested
Lua table**, and it reads both files whole. Two things about that break the
assumptions the interface was written under.

**A partial file is not a valid file.** The server does not merge what it finds
with its defaults; it parses what is there. So a `servertest.ini` assembled
from only the keys Garrison happens to model would silently delete the other
hundred-odd, including whatever an operator had set by hand.

**`Plan` and `Compile` are not independent.** The community container image's
`run_server.sh` calls `apply_postinstall_config` on every boot, which rewrites
thirteen config keys from environment variables: `SaveWorldEveryMinutes`,
`DefaultPort`, `UDPPort`, `MaxPlayers`, `Mods`, `Map`, `WorkshopItems`,
`PauseEmpty`, `Open`, `RCONPassword`, `RCONPort`, `PublicName` and `Password`.
Whatever `Compile` writes to those, the image overwrites from `Plan`'s
environment. The interface was designed as though a game's container and a
game's config files were separate concerns. For this game they are the same
concern seen twice.

## Decision

**`Compile` returns complete files**, generated from the game's own defaults
overlaid with the instance's settings. The plugin therefore carries every key
the game writes, which is why `internal/games/zomboid/keys_gen.go` is generated
from fixtures rather than hand-written: completeness is the requirement, and
413 hand-typed literals would not stay complete.

**`Plan`'s environment is derived from the same settings `Compile` reads**, for
every key the image reconciles. Not "kept in sync" — derived, from one source,
so they cannot drift.

The defaults come out of the game: `servertest.ini`'s key list and types from a
config the server generated, and the sandbox defaults from
`media/lua/shared/Sandbox/Apocalypse.lua`, which is what the game itself means
by "default". This follows [ADR 0008](0008-game-vocabularies-come-from-the-game.md).

## Consequences

- The settings form has 413 fields for this game. That is only usable because
  almost all of them are `Advanced`: the group rail plus `a` is the difference
  between a settings screen and a config file with borders.
- The apply diff earns its keep here in a way it could not for Valheim. A
  change to one sandbox variable is one line in a 269-line file, and seeing
  that before it is written is the difference between a confident apply and a
  hopeful one.
- **A hand edit to a key Garrison models is replaced on the next apply.** Both
  files say so in a header comment. This is the honest cost of whole-file
  generation, and the mitigation is that Garrison models *every* key, so a
  hand edit is representable as a setting rather than being unmodellable.
- `Compile` stays pure. The alternative below is what purity buys.
- Adding a game whose image also rewrites config means finding that out and
  mirroring it, which is manual and will be got wrong once. The test that
  catches it asserts `Plan`'s environment against the same settings `Compile`
  reads.

## Rejected

**Merge with the file on disk.** The obvious fix for whole-file writes: read
`servertest.ini`, change the keys we know, write it back. It makes `Compile`
do I/O, which means it is no longer a pure function of an instance, which
means the apply diff cannot be generated in a view and the settings form
cannot show you what a change does before you commit to it. The diff was worth
more than the merge.

**Model only the keys we expose in the form.** Half the size and it deletes
data. A config file is not ours to prune.

**Drive everything through the image's environment and compile nothing**, as
Valheim does. It would work for the thirteen keys the image handles and
nothing else, and it would leave M3 having tested `Compile` no more than M1
did. The point of a second game is to use the interface, not to route around
it.

## How we would know this was wrong

If a third game arrives whose image also rewrites config, and mirroring it in
`Plan` turns out to be more than a handful of keys, then the reconciliation
belongs in one place rather than in each plugin — probably a declaration on
`model.Plan` that says "these env vars are authoritative for these config
keys", checked by a test. One game is not enough to design that from.

The narrower risk is the mirror going stale: the image adds a key to
`apply_postinstall_config`, nobody notices, and a setting silently stops
applying. That would show up as a setting that reverts after a restart, which
is a bad way to find out.
