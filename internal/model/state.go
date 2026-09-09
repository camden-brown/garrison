package model

// State is a server's lifecycle state, collapsed to what the UI encodes.
//
// It lives in model rather than in internal/host because both ends of the
// program need it: host observes it from the container engine, and a view
// renders it. A view that had to import internal/host to name a state would be
// reaching across a layer boundary for a plain enum — the exact smell
// internal/arch exists to catch.
type State uint8

const (
	// StateUnknown means the engine could not be reached. It is deliberately
	// distinct from StateStopped: "I cannot see it" and "it is switched off"
	// are different facts, and the fleet view says which one it means.
	StateUnknown State = iota
	StateCreated
	StateRunning
	StateRestarting
	StateStopped
	StateCrashed
)

var stateNames = [...]string{
	"unknown", "created", "running", "restarting", "stopped", "crashed",
}

func (s State) String() string {
	if int(s) < len(stateNames) {
		return stateNames[s]
	}
	return "unknown"
}

// Live reports whether the server is running or on its way there, which is
// what decides whether stopping it is the offered action.
func (s State) Live() bool { return s == StateRunning || s == StateRestarting }
