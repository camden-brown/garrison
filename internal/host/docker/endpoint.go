// Package docker implements host.Driver against the Docker Engine API.
//
// Nothing above internal/host imports this package. It is selected in
// cmd/garrison and passed as a host.Driver, which is the seam that would let a
// remote, SSH or Podman driver drop in without touching a view.
package docker

import (
	"fmt"
	"runtime"
	"strings"
)

// The two endpoints that matter. Windows is the target platform; the unix
// socket is what development under WSL2 actually talks to, and it is also what
// CI has.
const (
	WindowsEndpoint = "npipe:////./pipe/docker_engine"
	UnixEndpoint    = "unix:///var/run/docker.sock"
)

// Origin says where a resolved endpoint came from, so an error can tell you
// which knob to turn and the status bar can show "npipe" rather than a URL.
type Origin string

const (
	OriginConfig  Origin = "config"          // garrison.toml or --docker-endpoint
	OriginGarEnv  Origin = "GARRISON_DOCKER" // GARRISON_DOCKER_HOST
	OriginDockEnv Origin = "DOCKER_HOST"     // the standard variable
	OriginDefault Origin = "default"         // the per-OS default below
)

// Env is the environment an endpoint is resolved against. Passing it in rather
// than reading os.Getenv keeps resolution a pure function, which is the only
// part of this package that can be tested with Docker stopped.
type Env struct {
	Garrison string // GARRISON_DOCKER_HOST
	Docker   string // DOCKER_HOST
	GOOS     string // empty means runtime.GOOS
}

// DefaultEndpoint is the endpoint for an operating system with nothing
// configured.
//
// This switches on GOOS at runtime rather than living in two build-tagged
// files on purpose. internal/arch parses imports with build constraints
// ignored (ADR 0005), so a pair of files that differ by platform is exactly
// the shape that makes the graph it builds a fiction. A string constant does
// not need a build tag to be correct.
func DefaultEndpoint(goos string) string {
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos == "windows" {
		return WindowsEndpoint
	}
	return UnixEndpoint
}

// Resolve picks the endpoint to dial.
//
// Precedence, highest first: the configured value (a flag or garrison.toml),
// GARRISON_DOCKER_HOST, DOCKER_HOST, then the per-OS default. DOCKER_HOST is
// honoured because a WSL2 shell or a rootless install has usually already set
// it correctly, and ignoring it would make Garrison the one tool on the
// machine that needs telling twice. GARRISON_DOCKER_HOST sits above it so a
// shell aimed at another engine does not drag the fleet along with it.
//
// A bare path is accepted and given a scheme, because that is what people type
// into a config file.
func Resolve(configured string, env Env) (endpoint string, origin Origin, err error) {
	switch {
	case strings.TrimSpace(configured) != "":
		endpoint, origin = configured, OriginConfig
	case strings.TrimSpace(env.Garrison) != "":
		endpoint, origin = env.Garrison, OriginGarEnv
	case strings.TrimSpace(env.Docker) != "":
		endpoint, origin = env.Docker, OriginDockEnv
	default:
		endpoint, origin = DefaultEndpoint(env.GOOS), OriginDefault
	}

	normalized, err := normalize(strings.TrimSpace(endpoint))
	if err != nil {
		return "", origin, fmt.Errorf("docker endpoint from %s: %w", origin, err)
	}
	return normalized, origin, nil
}

// normalize gives a bare path a scheme and rejects one Garrison cannot dial.
func normalize(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("empty endpoint")
	}

	scheme, rest, hasScheme := strings.Cut(s, "://")
	if !hasScheme {
		return guessScheme(s)
	}

	switch scheme {
	case "unix", "npipe", "tcp", "http", "https", "ssh":
		if rest == "" {
			return "", fmt.Errorf("%q has a scheme but no address", s)
		}
		return s, nil
	default:
		return "", fmt.Errorf("unsupported scheme %q in %q: want unix, npipe, tcp, http, https or ssh", scheme, s)
	}
}

// guessScheme turns the paths people actually write into endpoints.
//
//	\\.\pipe\docker_engine   -> npipe:////./pipe/docker_engine
//	//./pipe/docker_engine   -> npipe:////./pipe/docker_engine
//	/var/run/docker.sock     -> unix:///var/run/docker.sock
//	localhost:2375           -> tcp://localhost:2375
func guessScheme(s string) (string, error) {
	slashed := strings.ReplaceAll(s, `\`, "/")
	if strings.HasPrefix(strings.ToLower(slashed), "//./pipe/") {
		return "npipe://" + slashed, nil
	}
	if strings.HasPrefix(s, "/") {
		return "unix://" + s, nil
	}
	if strings.Contains(s, ":") {
		return "tcp://" + s, nil
	}
	return "", fmt.Errorf("cannot tell what %q is: give it a scheme, e.g. unix:// or npipe://", s)
}

// Transport names the endpoint's scheme for display: the status bar says
// "docker 28.1.1 · npipe · healthy", which is how you notice you are pointed
// at the wrong engine.
func Transport(endpoint string) string {
	if scheme, _, ok := strings.Cut(endpoint, "://"); ok {
		return scheme
	}
	return "unknown"
}
