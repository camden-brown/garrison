# 0012 — A mod is either an id a server resolves or a file Garrison installs

**Status:** Accepted · 2026-09-11

## Context

`games.Moddable` was written against Project Zomboid and assumes one shape:
the operator lists ids, `Apply` writes them into config, and the *server*
downloads them. Garrison never touches a mod file. That is genuinely how
Zomboid works — `WorkshopItems` and `Mods` are two keys in `servertest.ini` —
and it is why Valheim could not implement the interface at all.

Valheim has no Steam Workshop. Its mods are BepInEx plugins published on
thunderstore.io as zip archives, and a plugin is loaded because its DLL is in
a directory and for no other reason. There is no configuration key that names
a mod, so `Apply` has nothing to write, and no mechanism by which the server
would fetch one for itself.

[ADR 0006](0006-valheim-first.md) left this open deliberately: "whether the
interface needs an install path is a question for the first game that actually
has one; guessing now is what ADR 0006 warns against." Valheim is now that
game.

The `lloesche/valheim-server` image supplies most of the mechanism. Setting
`BEPINEX=true` makes it fetch `denikson-BepInExPack_Valheim` from Thunderstore,
install the loader and launch the server through it; on every boot it rsyncs
`/config/bepinex/plugins/` into the installation and symlinks BepInEx's config
directory back out to `/config/bepinex/`. What it does not do is fetch any
actual mods.

## Decision

**A second capability rather than a wider one.** `games.Installable` says
where a mod's files belong, relative to the instance's data volume — the same
frame of reference `model.File` already uses — and which ids the server image
installs for itself. A game implements it *in addition to* `Moddable`.

Fetching and unpacking live in `internal/services/mods`, not in the plugin: a
plugin's methods are pure and called on every render, and downloading tens of
megabytes is neither. The apply task gains an install step, bound the way a
drain is — `cmd` asserts the capability and adapts it, because `internal/tasks`
must not import `internal/games`.

**BepInEx follows from the mod list.** Valheim's `Plan` sets `BEPINEX` from
whether any mods are configured rather than from a setting of its own. A
loader with no plugins changes nothing but the startup path, and a plugin with
no loader is a file the server never reads; making them two switches only
creates a state where they disagree, and that state looks exactly like "mods
do not work".

## Consequences

- The Mods screen works for Valheim: names, versions and update badges, from
  Thunderstore's per-package endpoint. Resolving is hourly and one request per
  mod, because that API takes no list — affordable at tens of mods, and the
  reason a failed lookup marks its own row rather than failing the poll.
- Two versions are needed for an update badge and the source knows only one.
  The installer writes `.garrison-mods.json` beside the mods it installed, and
  that manifest is where "what is running" comes from.
- **The manifest is also what makes the directory safe to share.** Pruning
  removes what the manifest claims and nothing else, so a DLL somebody dropped
  in by hand survives every sync. Losing the manifest therefore orphans mods
  rather than deleting files, which is the right direction to fail in.
- The install step declares compensation, as any step that changes a volume
  must: displaced mods are *moved* to a sibling directory rather than copied,
  so undoing an install costs a rename. An apply that installs three mods and
  then cannot start the server puts all three back.
- A volume-backed server cannot install mods, for the same reason it cannot be
  archived (debt 3): there is no host path to write to. It refuses loudly
  rather than reporting an install that wrote nothing.
- Garrison does not resolve dependencies. Thunderstore packages declare them
  and the resolver reports them, but installing one does not pull the others
  in. A mod that needs Jotunn and does not say so in the TOML will not find
  it.

## Rejected

**Widening `Moddable` with an install path.** One interface, every game
answering "where do the files go" including the ones where the question is
meaningless. Zomboid would return an empty string and every call site would
have to know that empty means "this game does not work that way" — which is
the switch on game id that the capability interfaces exist to prevent.

**Installing into the live BepInEx directory.** It is inside the container's
image layer, not the volume, so it is destroyed on every recreate and rebuilt
by the image's own updater. Writing there means racing the thing that manages
it. The staging directory the image syncs from is both durable and the
supported seam.

**Unpacking the whole package.** A Thunderstore zip carries `manifest.json`,
an icon, a README, and sometimes a `BepInEx/config/` with the author's
defaults. Writing that last one over a server's configuration on every update
would silently revert the operator's settings, so only plugin content is
installed.

**Letting the image install mods too.** It has a generic mod installer, but it
is built for the loader packages — one URL, one install path — and it would
put Garrison's knowledge of what is installed in a place Garrison cannot read.
The update badge needs the manifest.

## How we would know this was wrong

If a third game arrives whose mods are files but whose layout is not "a
directory per package under one root" — per-mod install scripts, or a loader
that wants a flat directory — then `ModDir` is too small an answer and the
capability should have carried the layout rather than the path. The signal
would be an `Installable` implementation that lies about its directory to get
the files somewhere else.

The other way this is wrong is scope: if operators end up managing mods
through r2modman on the client and copying the folder to the server anyway,
then the feature solved a problem nobody had, and the honest response is to
delete the resolver and keep the BepInEx flag.
