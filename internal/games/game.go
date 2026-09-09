// Package games defines the plugin surface every supported game implements.
//
// A "plugin" here is a Go package compiled into the binary that registers
// itself in init. Go's plugin package does not support Windows, and
// compile-time registration is the better fit anyway: type safety across the
// interface, one file to ship, no ABI to version, and a rebuild takes about a
// second.
//
// This package must not import internal/host, internal/core, internal/tasks,
// internal/services or internal/tui. A game describes what it wants; other
// layers make it happen. See internal/arch.
package games

import "github.com/camden-brown/garrison/internal/model"

// Game is the entire required surface for a supported game. Every method must
// be cheap and free of side effects: Meta and Schema are called on every
// render, and Parse is called for every line of every running server.
//
// Everything beyond these five methods is an optional capability in caps.go,
// discovered by type assertion. A game implementing none of them still gets a
// dashboard, a console, settings, tasks and backups.
type Game interface {
	// Meta is identity and defaults.
	Meta() Meta

	// Plan is the container this instance should be. Pure — no Docker calls.
	Plan(inst model.Instance) (model.Plan, error)

	// Schema is the typed settings the TUI renders as a form.
	Schema() Schema

	// Compile turns an instance's settings into the files the game reads.
	// Paths are relative to the data volume so the caller can diff before
	// writing.
	Compile(inst model.Instance) ([]model.File, error)

	// Parse classifies one line of server output. Return a zero Event to
	// drop the line. Hot path: keep it allocation-light.
	Parse(line string) model.Event
}

// Meta is a game's identity and its defaults.
type Meta struct {
	ID           string // "zomboid" — stable, used in config files and labels
	Name         string // "Project Zomboid"
	SteamAppID   string // dedicated server app id, "" if not on Steam
	DefaultPorts []model.PortMap
	DefaultImage string

	// MetricLabel names the fourth dashboard tile, e.g. "zombies alive".
	// Empty means the tile shows network I/O instead.
	MetricLabel string
	// MetricKey is the Event.Metric this game emits for that tile.
	MetricKey string
}
