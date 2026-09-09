package games

import "context"

// Conn is a live connection to a running instance, supplied by the caller.
//
// Games use it to talk to a server and never dial anything themselves, which
// keeps every capability in caps.go testable with a fake and keeps network
// code out of the plugins.
type Conn interface {
	// RCON sends a command over the instance's RCON connection. It returns
	// an error for games that have no RCON.
	RCON(ctx context.Context, cmd string) (string, error)

	// Exec runs a command inside the instance's container.
	Exec(ctx context.Context, argv []string) ([]byte, error)

	// HTTP performs a request against the instance's own HTTP API, for the
	// games that have one.
	HTTP(ctx context.Context, method, path string, body []byte) ([]byte, error)

	// Addr is the instance's host:port, for out-of-band queries.
	Addr() string
}
