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

// Mod is a resolved mod: what a source told us about a ModRef.
type Mod struct {
	ID        string
	Name      string
	Version   string
	Available string // latest known version, for the update badge
	Enabled   bool
	SizeBytes int64
	Requires  []string // other mod IDs
	Changelog string
}
