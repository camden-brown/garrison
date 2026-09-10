package model

// Mod is a mod as resolved: what a source told us about a ModRef.
//
// It lives in model rather than in internal/games because the snapshot
// carries it to the Mods view and core imports no game package — the same
// reason Archive and Session are here. internal/games keeps the name as an
// alias, so it reads as games.Mod where a plugin uses it.
type Mod struct {
	// ID and Pin come from the server's TOML; everything else is what a
	// resolver found out.
	ID  string
	Pin string

	Name    string
	Version string
	// Available is the newest version the source knows about, which is what
	// makes an update badge possible. Empty means nobody has looked.
	Available string

	Enabled   bool
	SizeBytes int64
	Requires  []string // other mod IDs
	Changelog string

	// Err is why this mod could not be resolved, empty when it was. A mod
	// the Workshop has never heard of is worth showing as a problem rather
	// than omitting from a list the operator wrote themselves.
	Err string
}

// NeedsUpdate reports whether the source knows a newer version than the one
// in use.
//
// A pinned mod never needs an update: the pin is the operator saying which
// version they want, and a badge nagging about a deliberate choice is noise.
func (m Mod) NeedsUpdate() bool {
	if m.Pin != "" || m.Available == "" || m.Version == "" {
		return false
	}
	return m.Available != m.Version
}
