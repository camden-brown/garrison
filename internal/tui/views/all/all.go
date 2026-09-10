// Package all registers every view, in rail order.
//
// Adding a screen to Garrison is: write internal/tui/views/<name>, then swap
// its line here. The rail order, the number keys and the help overlay all
// derive from this slice, so nothing else changes — which is the point of the
// View interface existing at all.
//
// Every screen in the rail is now built. internal/tui/views/stub is kept, and
// not because it is unused: the next screen starts as one, and a navigation
// model you can only half use is hard to judge — an entry that leads nowhere
// is indistinguishable from one that is broken.
//
// What replaced the last stubs is not "the screens exist" but that each one
// can say why it is empty. Mods has no table for Valheim because Valheim
// implements no games.Moddable, and it says so in Valheim's own terms.
package all

import (
	"github.com/camden-brown/garrison/internal/tui"
	"github.com/camden-brown/garrison/internal/tui/views/backups"
	"github.com/camden-brown/garrison/internal/tui/views/console"
	"github.com/camden-brown/garrison/internal/tui/views/dashboard"
	"github.com/camden-brown/garrison/internal/tui/views/fleet"
	"github.com/camden-brown/garrison/internal/tui/views/mods"
	"github.com/camden-brown/garrison/internal/tui/views/players"
	"github.com/camden-brown/garrison/internal/tui/views/settings"
	"github.com/camden-brown/garrison/internal/tui/views/tasks"
)

// Views returns a fresh set. They are values with their own state, so each
// call builds a new one rather than handing out shared cursors.
func Views() []tui.View {
	return []tui.View{
		fleet.New(),
		dashboard.New(),
		console.New(),
		players.New(),
		mods.New(),
		settings.New(),
		tasks.New(),
		backups.New(),
	}
}
