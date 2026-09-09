// Package all registers every view, in rail order.
//
// Adding a screen to Garrison is: write internal/tui/views/<name>, then swap
// its line here. The rail order, the number keys and the help overlay all
// derive from this slice, so nothing else changes — which is the point of the
// View interface existing at all.
//
// The unbuilt screens are stubs rather than absences. A navigation model you
// can only half use is hard to judge, and an entry that leads nowhere is
// indistinguishable from one that is broken.
package all

import (
	"github.com/camden-brown/garrison/internal/tui"
	"github.com/camden-brown/garrison/internal/tui/views/dashboard"
	"github.com/camden-brown/garrison/internal/tui/views/fleet"
	"github.com/camden-brown/garrison/internal/tui/views/settings"
	"github.com/camden-brown/garrison/internal/tui/views/stub"
)

// Views returns a fresh set. They are values with their own state, so each
// call builds a new one rather than handing out shared cursors.
func Views() []tui.View {
	return []tui.View{
		fleet.New(),
		dashboard.New(),
		stub.New(tui.ViewConsole, "Console", "M2",
			"Classified log lines with a command input that routes to RCON, to container stdin, "+
				"or explains why neither is available. The log pipeline behind it already runs — "+
				"the dashboard's console tail is the same events."),
		stub.New(tui.ViewPlayers, "Players", "M4",
			"Who is on now, seven days of sessions, and occupancy by hour so a restart window "+
				"can be picked that bothers nobody. The live roster already exists on the dashboard; "+
				"the history needs somewhere durable to live, which is M2's SQLite."),
		stub.New(tui.ViewMods, "Mods", "M4",
			"Load order you can reorder, version and update checks, and conflict detection. "+
				"A game with no mod system gets an explanation here rather than an empty table."),
		settings.New(),
		stub.New(tui.ViewTasks, "Tasks", "M2",
			"The running task expanded to its steps, with its schedule and history. Restarts, "+
				"updates, mod syncs and backups become durable step sequences with declared rollback."),
		stub.New(tui.ViewBackups, "Backups", "M2",
			"Snapshots, sizes and restore. Restoring over a live save is one of the three actions "+
				"that asks you to type the server's name."),
	}
}
