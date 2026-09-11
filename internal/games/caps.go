package games

import (
	"context"
	"time"

	"github.com/camden-brown/garrison/internal/model"
)

// The interfaces below are optional. Call sites assert for them and degrade
// with an explanation rather than an empty screen:
//
//	if r, ok := g.(games.Rostered); ok { ... }
//
// Adding one of these to a game is the whole cost of lighting up a feature.
// Reaching for a switch on Meta().ID anywhere outside this package means a
// capability interface is missing — that is the smell that matters most.

// Rostered reports who is connected.
type Rostered interface {
	Roster(ctx context.Context, c Conn) ([]model.Player, error)
}

// Commandable has an interactive command channel, which drives the console
// input line and its tab completion.
type Commandable interface {
	Command(ctx context.Context, c Conn, cmd string) (string, error)
	Complete(prefix string) []string
}

// Drainable can warn players before a shutdown and flush the world.
type Drainable interface {
	Warn(ctx context.Context, c Conn, in time.Duration) error
	Save(ctx context.Context, c Conn) error
}

// Moddable supports mods from some source.
type Moddable interface {
	ModSource() ModSource
	// Apply returns the config files that declare the given mods. Most games
	// list mods in config rather than by directory contents.
	Apply(inst model.Instance, mods []Mod) ([]model.File, error)
	// LoadOrderMatters drives whether the Mods view numbers and reorders.
	LoadOrderMatters() bool
}

// Installable is a game whose mods are files Garrison has to put in place,
// rather than ids a server resolves for itself.
//
// Moddable alone assumes the second shape: Zomboid is given Workshop ids and
// downloads them, so Apply writes two config keys and the server does the
// rest. Valheim's BepInEx plugins are DLLs in a directory and no amount of
// configuration will make the server fetch one — which is the gap ADR 0006
// declined to guess at and ADR 0012 closes now that a game needs it.
//
// A game implements this in addition to Moddable. Fetching and unpacking is
// not here: a plugin's methods are pure, and this says only where the files
// belong.
type Installable interface {
	// ModDir is where an installed mod's files go, relative to the
	// instance's data volume — the same frame of reference as model.File,
	// so a plugin never learns where that volume is mounted.
	ModDir(inst model.Instance) string
	// Bundled reports mods the server image installs itself, which Garrison
	// must not install over. For Valheim that is the BepInEx pack: the
	// image downloads and updates it, and a second copy underneath is how
	// you get two loaders arguing about the same assembly.
	Bundled() []string
	// ClientSteps is how a player installs these mods on their own machine,
	// one line per step, for the text the "y" share puts on the clipboard.
	//
	// It is here because a game where the mods are files is exactly the
	// game where every player has to install them too — the server cannot
	// send them, and a version mismatch is refused at connect. Which steps
	// those are is the plugin's knowledge: the shell must not learn that
	// Valheim means BepInEx and r2modman.
	ClientSteps() []string
}

// Backupable knows which paths are the save, and how to make a hot copy
// consistent.
type Backupable interface {
	SavePaths(inst model.Instance) []string
	// Quiesce pauses writes. The returned func must always be safe to call.
	Quiesce(ctx context.Context, c Conn) (release func(), err error)
}

// Probeable has a health signal better than the container healthcheck.
type Probeable interface {
	Probe(ctx context.Context, c Conn) (model.Health, error)
}

// ModSource picks the resolver used to look mods up and check for updates.
type ModSource string

const (
	ModSourceNone         ModSource = ""
	ModSourceWorkshop     ModSource = "workshop"
	ModSourceThunderstore ModSource = "thunderstore"
	ModSourceURL          ModSource = "url"
)

// Mod is a resolved mod: what a source told us about a ModRef. It is
// model.Mod under another name, because the snapshot carries these to the
// Mods view and core imports no game package.
type Mod = model.Mod
