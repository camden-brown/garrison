// Package all registers every view, in rail order.
//
// Adding a screen to Garrison is: write internal/tui/views/<name>, then add
// one line here. The rail order, the number keys and the help overlay all
// derive from this slice, so nothing else changes — which is the point of the
// View interface existing at all.
package all

import (
	"github.com/camden-brown/garrison/internal/tui"
	"github.com/camden-brown/garrison/internal/tui/views/dashboard"
	"github.com/camden-brown/garrison/internal/tui/views/fleet"
)

// Views returns a fresh set. They are values with their own cursors, so each
// call builds a new one rather than handing out shared state.
func Views() []tui.View {
	return []tui.View{
		fleet.New(),
		dashboard.New(),
	}
}
