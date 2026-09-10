# 0008 — A plugin's option lists are extracted from the game, not transcribed

**Status:** Accepted · 2026-09-09

## Context

Valheim's world modifiers are the first setting whose *values* are a closed
vocabulary the game owns: `-modifier deathpenalty casual`, five categories over
thirteen options. Every source for that list outside the game is secondhand —
wikis, host control-panel documentation, forum posts — and all of them are
written against whatever version the author had.

Getting a value wrong is not a loud failure. Valheim parses `-modifier` with a
case-insensitive `Enum.TryParse` on each argument and, when the pair parses but
the game has no slider for that combination, `ServerOptionsGUI.SetPreset` logs
`Missing settings for preset` and returns. The server starts, reports nothing
unusual, and runs with the setting unapplied. A typo in a schema would present
as a difficulty setting that appears configured and silently is not.

The same shape will recur. Zomboid at M3 has enumerated ini values, Palworld at
M4 has a settings struct, and both will outlive whatever documentation we read
while implementing them.

## Decision

A plugin's closed vocabularies are extracted from the game's own files, checked
into `testdata/` as a fixture with its provenance, and asserted against by test.
The schema may expose a subset; it may not contain a value the fixture does not.

For Valheim this is `internal/games/valheim/testdata/world-modifiers.txt`: the
`WorldModifiers`, `WorldModifierOption` and `GlobalKeys` enums read out of the
.NET metadata tables of `assembly_valheim.dll`, from the build the
`lloesche/valheim-server` image's SteamCMD bootstrap downloads.

## Consequences

- A Valheim update that renames or removes an option fails
  `TestModifierKeysAreValheimsOwn` on the next regeneration, rather than
  surfacing as a server quietly ignoring its own settings.
- The fixture records a *version* — `l-1.0.7`, network version 39. When it is
  regenerated the diff says what the game changed, which is the thing worth
  knowing.
- Extraction is a manual step, not part of the build. It needs the game files,
  which means a booted container and a ~2 GB download. Automating it would put
  a Steam download in CI to check a list that changes twice a year.
- `Plan` emits a modifier only when its value matches an option the schema
  declared. This is not only about typos: the image expands `$SERVER_ARGS`
  unquoted into the command line, so a hand-edited TOML value containing spaces
  would otherwise split into arguments of its own.

## Rejected

**Transcribing the documented list.** Cheaper, correct today, and gives nothing
that fails when it stops being correct. The failure mode it leaves in place —
a setting that reads as applied and is not — is the specific thing worth paying
to avoid.

**Reading the vocabulary from the container at render time.** `Schema()` is pure
and called on every render, and `games` must not import `host`. A plugin
describes the container it wants; it does not interrogate one.

**Extracting the per-category option subsets too.** Which options a category
accepts lives in Unity serialized asset data rather than in the assembly, and
reaching it means an asset-bundle parser. The subsets in the schema are
therefore still documentation-derived; only the vocabulary is extracted. The
mitigation is that a wrong subset degrades to the same visible symptom as
before — the value is offered, the server logs it, nothing applies — and the
extracted enum still bounds what can be emitted at all.

## How we would know this was wrong

If regenerating the fixture becomes onerous enough that it stops happening, it
will drift from the game and give false confidence — worse than no fixture,
because the test will still pass. Mitigation: the fixture header carries the
command to regenerate it and the version it came from, so a stale one is
visible rather than inferred.
