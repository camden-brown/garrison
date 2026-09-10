// Package conn is the transport a game plugin talks to its server over.
//
// games.Conn is declared in the games package and implemented here, which is
// the whole point of the split: a plugin says "ask the server who is on" and
// never opens a socket, so every capability in caps.go is testable against a
// fake and no network code lives in a plugin. This package may import host —
// container exec is one of the three channels — and games may not, which is
// why the implementation is on this side of the line.
package conn

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// Source RCON, which is what Zomboid speaks.
//
// The protocol is four little-endian int32s and two null bytes:
//
//	size | id | type | body\0 \0
//
// where size counts everything after itself. Three types matter: 3 to
// authenticate, 2 to run a command, and 0 for a response. An auth reply comes
// back as type 2 with the request id echoed, or -1 to mean the password was
// wrong — and that -1 is the only way to tell a bad password from a working
// connection, because the server closes the socket either way.
const (
	typeResponse = 0
	typeCommand  = 2
	typeAuth     = 3

	// maxPacket bounds what will be read for one reply. A server that claims
	// a gigabyte is a server that is not speaking RCON, and allocating for
	// it is how a game's log flood becomes an out-of-memory.
	maxPacket = 4 << 20

	// dialTimeout and ioTimeout are separate because they fail differently:
	// a server still loading its world refuses the connection quickly, and
	// one mid-save accepts and then says nothing.
	dialTimeout = 5 * time.Second
	ioTimeout   = 10 * time.Second
)

// ErrAuth is a rejected password, which is worth telling apart from a server
// that is not listening: one is a settings problem and the other is a state
// problem, and an operator chasing the wrong one wastes an evening.
var ErrAuth = errors.New("rcon: authentication failed")

// RCON is a lazily-connected, serialised RCON client for one server.
//
// Serialised because the protocol has no way to match a reply to a request
// beyond an id the server is not required to echo, so two commands in flight
// on one socket cannot be untangled. The mutex is the exception ADR 0004
// allows outside the store: it guards a socket, not shared state, and it is
// per-instance.
type RCON struct {
	Addr     string
	Password string

	// Dial is overridable so the tests can drive a real protocol exchange
	// against an in-process server rather than a mock of one.
	Dial func(ctx context.Context, addr string) (net.Conn, error)

	mu   sync.Mutex
	conn net.Conn
	// br is created once per connection and never per read. A buffered
	// reader reads ahead, so building one per packet throws away whatever
	// it had already pulled off the socket — which is the next reply.
	br   *bufio.Reader
	next int32
}

// Close drops the connection. Safe to call on one that was never opened.
func (r *RCON) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.drop()
}

func (r *RCON) drop() error {
	if r.conn == nil {
		return nil
	}
	err := r.conn.Close()
	r.conn, r.br = nil, nil
	return err
}

// Exec runs one command and returns what the server said.
//
// A dead connection is reconnected once and the command retried, because the
// common failure is a socket the server closed while nothing was using it —
// a restart, an idle timeout — and making the caller handle that would mean
// every capability in caps.go handling it identically.
func (r *RCON) Exec(ctx context.Context, cmd string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	out, err := r.exec(ctx, cmd)
	if err == nil || errors.Is(err, ErrAuth) || ctx.Err() != nil {
		return out, err
	}

	// One retry on a fresh socket. Anything that fails twice is a real
	// failure and is reported as one.
	_ = r.drop()
	return r.exec(ctx, cmd)
}

func (r *RCON) exec(ctx context.Context, cmd string) (string, error) {
	if err := r.connect(ctx); err != nil {
		return "", err
	}

	id := r.id()
	if err := r.write(id, typeCommand, cmd); err != nil {
		return "", fmt.Errorf("rcon %s: sending %q: %w", r.Addr, cmd, err)
	}

	_, _, body, err := r.read()
	if err != nil {
		return "", fmt.Errorf("rcon %s: reading the reply to %q: %w", r.Addr, cmd, err)
	}
	return body, nil
}

func (r *RCON) connect(ctx context.Context) error {
	if r.conn != nil {
		return nil
	}

	dial := r.Dial
	if dial == nil {
		dial = func(ctx context.Context, addr string) (net.Conn, error) {
			d := net.Dialer{Timeout: dialTimeout}
			return d.DialContext(ctx, "tcp", addr)
		}
	}

	c, err := dial(ctx, r.Addr)
	if err != nil {
		return fmt.Errorf("rcon %s: %w", r.Addr, err)
	}
	r.conn, r.br = c, bufio.NewReader(c)

	id := r.id()
	if err := r.write(id, typeAuth, r.Password); err != nil {
		_ = r.drop()
		return fmt.Errorf("rcon %s: sending the password: %w", r.Addr, err)
	}

	// Some servers answer an auth with an empty response packet first and
	// the verdict second. Reading until a non-response packet arrives
	// handles both without guessing which one this is.
	for {
		gotID, gotType, _, err := r.read()
		if err != nil {
			_ = r.drop()
			return fmt.Errorf("rcon %s: reading the password verdict: %w", r.Addr, err)
		}
		if gotType == typeResponse {
			continue
		}
		if gotID == -1 {
			_ = r.drop()
			return fmt.Errorf("%w for %s", ErrAuth, r.Addr)
		}
		return nil
	}
}

func (r *RCON) id() int32 {
	r.next++
	if r.next < 1 {
		r.next = 1
	}
	return r.next
}

func (r *RCON) write(id, kind int32, body string) error {
	if err := r.conn.SetWriteDeadline(time.Now().Add(ioTimeout)); err != nil {
		return err
	}

	var b bytes.Buffer
	size := int32(4 + 4 + len(body) + 2)
	binary.Write(&b, binary.LittleEndian, size)
	binary.Write(&b, binary.LittleEndian, id)
	binary.Write(&b, binary.LittleEndian, kind)
	b.WriteString(body)
	b.WriteByte(0)
	b.WriteByte(0)

	_, err := r.conn.Write(b.Bytes())
	return err
}

func (r *RCON) read() (id, kind int32, body string, err error) {
	if err := r.conn.SetReadDeadline(time.Now().Add(ioTimeout)); err != nil {
		return 0, 0, "", err
	}
	br := r.br

	var size int32
	if err := binary.Read(br, binary.LittleEndian, &size); err != nil {
		return 0, 0, "", err
	}
	if size < 10 || size > maxPacket {
		return 0, 0, "", fmt.Errorf("implausible packet size %d", size)
	}
	if err := binary.Read(br, binary.LittleEndian, &id); err != nil {
		return 0, 0, "", err
	}
	if err := binary.Read(br, binary.LittleEndian, &kind); err != nil {
		return 0, 0, "", err
	}

	payload := make([]byte, size-8)
	if _, err := io.ReadFull(br, payload); err != nil {
		return 0, 0, "", err
	}
	// Two trailing nulls: the body's terminator and the packet's.
	return id, kind, strings.TrimRight(string(payload), "\x00"), nil
}
