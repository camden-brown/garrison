package games

// Schema is a game's settings surface. The TUI renders a form from it and
// contains no game-specific code: type drives the editor, Min and Max drive
// validation and the stepper, Impact drives the badge, the confirm modal and
// whether a restart task follows.
//
// Adding a setting to a game is one Field here and nothing else.
type Schema struct {
	Fields []Field
}

// Field is one setting.
type Field struct {
	Key      string // the game's own key: "MaxPlayers", "ZombieConfig.Speed"
	Label    string // what a person calls it: "Max players"
	Group    string // "Gameplay" — becomes a group in the settings rail
	Type     Type
	Default  any
	Min, Max any      // numeric and duration types only
	Options  []Option // TypeEnum only
	Help     string   // two lines at most; always visible under the focused field
	Impact   Impact
	Advanced bool // hidden until the user asks, to keep the common form short
}

// Option is one choice for a TypeEnum field.
type Option struct {
	Value any
	Label string
	Note  string
}

// Type selects the editor and the validation.
type Type uint8

const (
	TypeBool Type = iota
	TypeInt
	TypeFloat
	TypeString
	TypeEnum
	TypeDuration
	TypeList
	// TypeSecret is a password. The form masks it and will not let one be
	// typed, because the only place it could put one is the server's TOML
	// file — in plain text, beside everything else. That is where a secret
	// lives today and the file should be treated accordingly; DESIGN's
	// Credential Manager store is not built. Marking a field secret buys
	// masking on screen and nothing more, which is the honest reading of it
	// until ADR 0009 is revisited.
	TypeSecret
)

// Impact is what applying a change costs.
type Impact uint8

const (
	// ImpactLive applies without interrupting play.
	ImpactLive Impact = iota
	// ImpactRestart needs the process bounced; offers a drain.
	ImpactRestart
	// ImpactRecreate needs a new container: ports, mounts, limits, env.
	ImpactRecreate
	// ImpactWipeRisk can destroy a world. Confirming requires typing the
	// server name, and a backup is taken first, unconditionally.
	ImpactWipeRisk
)

// Groups returns the distinct group names in field order.
func (s Schema) Groups() []string {
	seen := make(map[string]bool, len(s.Fields))
	out := make([]string, 0, 8)
	for _, f := range s.Fields {
		if !seen[f.Group] {
			seen[f.Group] = true
			out = append(out, f.Group)
		}
	}
	return out
}

// Field looks up a field by key.
func (s Schema) Field(key string) (Field, bool) {
	for _, f := range s.Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

// MaxImpact is the highest impact among the given keys, which decides what
// applying a batch of changes actually does.
func (s Schema) MaxImpact(keys []string) Impact {
	worst := ImpactLive
	for _, k := range keys {
		if f, ok := s.Field(k); ok && f.Impact > worst {
			worst = f.Impact
		}
	}
	return worst
}
