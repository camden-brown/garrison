package valheim

import (
	"github.com/camden-brown/garrison/internal/games"
	"github.com/camden-brown/garrison/internal/model"
)

// Valheim's mods are files, which is the shape Moddable was not built for
// until Installable joined it. See ADR 0012.
var (
	_ games.Moddable    = Game{}
	_ games.Installable = Game{}
)

// ModSource is Thunderstore, which is where the Valheim modding community
// actually publishes. There is no Steam Workshop for Valheim at all.
func (Game) ModSource() games.ModSource { return games.ModSourceThunderstore }

// LoadOrderMatters is false, and the reason is worth stating: BepInEx sorts
// plugins by the dependency graph each one declares in its own attributes, so
// the order they were listed in is not an input to anything. Numbering them in
// the Mods view would be inventing a fact.
func (Game) LoadOrderMatters() bool { return false }

// Apply returns no files, because nothing in Valheim's configuration names a
// mod.
//
// A BepInEx plugin is loaded because its DLL is in the plugins directory and
// for no other reason — presence is the declaration. That is exactly why
// Installable exists: the work this method does for Zomboid is done by the
// install step for Valheim, and returning an empty slice here is honest
// rather than a stub.
func (Game) Apply(model.Instance, []games.Mod) ([]model.File, error) { return nil, nil }

// ModDir is where the image looks for plugins to sync into the loader.
//
// The lloesche image rsyncs /config/bepinex/plugins/ into the BepInEx
// installation on every boot, and symlinks BepInEx's config directory to
// /config/bepinex/. So this is the staging directory, not the live one: a
// server can be running while it is written, and the change takes effect at
// the next start — which is why the install step does not need the server
// stopped.
func (Game) ModDir(model.Instance) string { return "bepinex/plugins" }

// ClientSteps is what a player has to do before they can join.
//
// The third step is the one that matters. Launching from Steam runs the game
// unmodded, the mods here use ServerSync, and ServerSync refuses a client
// whose versions do not match — so the symptom of skipping it is a connection
// that fails for no visible reason.
func (Game) ClientSteps() []string {
	return []string{
		"Install r2modman: https://thunderstore.io/package/ebkr/r2modman/",
		"Make a Valheim profile in it and install the mods above, at those exact versions.",
		"Launch Valheim from r2modman, not from Steam — Steam runs it unmodded and the server will refuse you.",
	}
}

// Bundled is the BepInEx pack itself. The image downloads it from Thunderstore
// on every update, so Garrison installing it as a mod would leave two copies
// of the loader fighting over the same doorstop entry point.
func (Game) Bundled() []string {
	return []string{"denikson-BepInExPack_Valheim"}
}
