package conn

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/camden-brown/garrison/internal/games"
	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/model"
)

// Conn is one server's transport, implementing games.Conn.
//
// Three channels, and a game uses whichever it has: RCON for the games with a
// command protocol, container exec for the ones where the only way in is a
// process, and HTTP for the ones with a REST API. A game that has none of them
// never calls any of this, because it implements none of the capabilities that
// take a Conn.
type Conn struct {
	driver    host.Driver
	container string
	addr      string

	rcon *RCON
}

var _ games.Conn = (*Conn)(nil)

// ErrNoRCON is what a game gets when it asks for a channel this server does
// not have, which is a different thing from one that is down.
var ErrNoRCON = errors.New("this server has no RCON channel configured")

// For builds the transport for one server from its plan and its container.
//
// The plan says which port carries RCON and what the password is — the plugin
// declares both, so nothing here switches on a game id. The container says
// where that port actually landed on the host, which is the half a plan cannot
// know.
func For(driver host.Driver, c host.Container, plan model.Plan) *Conn {
	out := &Conn{
		driver:    driver,
		container: c.ID,
		addr:      hostAddr(c.Ports, plan.Ports),
	}

	if !plan.HasRCON() {
		return out
	}
	port, ok := publishedPort(c.Ports, plan.RCON.ContainerPort)
	if !ok {
		// The plan wants RCON on a port the container does not publish.
		// Leaving rcon nil makes that a clear "no channel" rather than a
		// connection that times out against nothing.
		return out
	}

	out.rcon = &RCON{
		Addr:     "127.0.0.1:" + strconv.Itoa(port),
		Password: plan.RCON.Password,
	}
	return out
}

// RCON sends a command over the server's RCON connection.
func (c *Conn) RCON(ctx context.Context, cmd string) (string, error) {
	if c == nil || c.rcon == nil {
		return "", ErrNoRCON
	}
	return c.rcon.Exec(ctx, cmd)
}

// Exec runs a command inside the container.
func (c *Conn) Exec(ctx context.Context, argv []string) ([]byte, error) {
	if c == nil || c.driver == nil || c.container == "" {
		return nil, errors.New("no container to run a command in")
	}
	return c.driver.Exec(ctx, c.container, argv)
}

// HTTP performs a request against the instance's own API, for the games that
// have one.
func (c *Conn) HTTP(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	if c == nil || c.addr == "" {
		return nil, errors.New("no address to reach this server on")
	}
	// Declared but unused until a game with a REST API arrives — Palworld is
	// the one DESIGN names. Returning a clear refusal rather than a broken
	// request means the first game to need it finds an honest gap rather
	// than a subtly wrong client.
	_ = http.MethodGet
	_ = method
	_ = path
	_ = body
	return nil, errors.New("HTTP transport is not implemented: no game needs it yet")
}

// Addr is the instance's host:port, for out-of-band queries.
func (c *Conn) Addr() string {
	if c == nil {
		return ""
	}
	return c.addr
}

// Close releases the RCON socket.
func (c *Conn) Close() error {
	if c == nil || c.rcon == nil {
		return nil
	}
	return c.rcon.Close()
}

// hostAddr is where the game itself is reachable: the first published port
// that is not the RCON one, which is the game port for every game so far.
func hostAddr(published, planned []model.PortMap) string {
	for _, p := range planned {
		if strings.HasSuffix(p.Container, "/udp") {
			if port, ok := publishedPort(published, p.Container); ok {
				return "127.0.0.1:" + strconv.Itoa(port)
			}
		}
	}
	for _, p := range published {
		return "127.0.0.1:" + strconv.Itoa(p.Host)
	}
	return ""
}

func publishedPort(published []model.PortMap, container string) (int, bool) {
	for _, p := range published {
		if p.Container == container {
			return p.Host, true
		}
	}
	return 0, false
}

// Pool keeps one Conn per server so the RCON socket is reused across calls.
//
// Without it every roster poll would open, authenticate and close a
// connection, which triples the round trips on the one thing that runs on a
// timer. The pool is keyed by container id, so a recreated container gets a
// fresh connection rather than one pointing at a port that has moved.
type Pool struct {
	Driver host.Driver

	mu    sync.Mutex
	conns map[string]*pooled
}

type pooled struct {
	container string
	conn      *Conn
	planHash  string
}

// Get returns the transport for a server, building it if the container or the
// plan has changed since last time.
func (p *Pool) Get(c host.Container, plan model.Plan) *Conn {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.conns == nil {
		p.conns = map[string]*pooled{}
	}

	hash := host.PlanHash(plan)
	if existing, ok := p.conns[c.Instance]; ok {
		if existing.container == c.ID && existing.planHash == hash {
			return existing.conn
		}
		// The container was recreated or the plan moved. The old socket
		// points somewhere that no longer exists.
		_ = existing.conn.Close()
	}

	built := For(p.Driver, c, plan)
	p.conns[c.Instance] = &pooled{container: c.ID, conn: built, planHash: hash}
	return built
}

// Forget drops a server's connection, for a delete or a stop.
func (p *Pool) Forget(instance string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if existing, ok := p.conns[instance]; ok {
		_ = existing.conn.Close()
		delete(p.conns, instance)
	}
}

// Close releases every connection.
func (p *Pool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for name, existing := range p.conns {
		_ = existing.conn.Close()
		delete(p.conns, name)
	}
	return nil
}

// timeout bounds one capability call. A game's server is single-threaded and
// a command issued while it is saving will simply wait, so this is generous
// rather than tight — but not unbounded, because a poller that blocks forever
// stops polling.
const timeout = 15 * time.Second

// WithTimeout is the context a capability call should run under.
func WithTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, timeout)
}

// Describe is a human-readable summary of what channels a server has, for an
// error message that would otherwise say only "failed".
func (c *Conn) Describe() string {
	if c == nil {
		return "no transport"
	}
	var have []string
	if c.rcon != nil {
		have = append(have, "rcon "+c.rcon.Addr)
	}
	if c.container != "" {
		have = append(have, "exec")
	}
	if len(have) == 0 {
		return fmt.Sprintf("no channels for %s", c.addr)
	}
	return strings.Join(have, ", ")
}
