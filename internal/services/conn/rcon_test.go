package conn_test

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/services/conn"
)

// fakeRCON is a real Source RCON server, in process.
//
// A mock of the client's own calls would prove nothing about the protocol:
// the packet framing, the two trailing nulls, and the -1 that means a bad
// password are exactly the parts worth being wrong about. This speaks the
// wire format so the test exercises it.
type fakeRCON struct {
	password string

	// reply is what the server answers a command with.
	reply string
	// authQuiet sends an empty response packet before the auth verdict,
	// which some servers do and the client has to see past.
	authQuiet bool
	// closeAfter drops the connection after this many commands, to drive
	// the reconnect path.
	closeAfter int

	listener net.Listener
	commands atomic.Int32
	authed   atomic.Int32
}

func (f *fakeRCON) start(t *testing.T) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.listener = l
	t.Cleanup(func() { l.Close() })

	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return l.Addr().String()
}

func (f *fakeRCON) serve(c net.Conn) {
	defer c.Close()

	handled := 0
	for {
		id, kind, body, err := readPacket(c)
		if err != nil {
			return
		}

		switch kind {
		case 3: // auth
			f.authed.Add(1)
			if f.authQuiet {
				writePacket(c, 0, 0, "")
			}
			if body != f.password {
				writePacket(c, -1, 2, "")
				return
			}
			writePacket(c, id, 2, "")

		case 2: // command
			f.commands.Add(1)
			handled++
			writePacket(c, id, 0, f.reply)
			if f.closeAfter > 0 && handled >= f.closeAfter {
				return
			}
		}
	}
}

func writePacket(w io.Writer, id, kind int32, body string) {
	size := int32(4 + 4 + len(body) + 2)
	binary.Write(w, binary.LittleEndian, size)
	binary.Write(w, binary.LittleEndian, id)
	binary.Write(w, binary.LittleEndian, kind)
	w.Write([]byte(body))
	w.Write([]byte{0, 0})
}

func readPacket(r io.Reader) (id, kind int32, body string, err error) {
	var size int32
	if err = binary.Read(r, binary.LittleEndian, &size); err != nil {
		return
	}
	if err = binary.Read(r, binary.LittleEndian, &id); err != nil {
		return
	}
	if err = binary.Read(r, binary.LittleEndian, &kind); err != nil {
		return
	}
	payload := make([]byte, size-8)
	if _, err = io.ReadFull(r, payload); err != nil {
		return
	}
	return id, kind, strings.TrimRight(string(payload), "\x00"), nil
}

func TestRCONRoundTrip(t *testing.T) {
	server := &fakeRCON{password: "secret", reply: "Players connected (2):\n-Huldra\n-Bjorn"}
	addr := server.start(t)

	client := &conn.RCON{Addr: addr, Password: "secret"}
	defer client.Close()

	got, err := client.Exec(context.Background(), "players")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(got, "Huldra") {
		t.Errorf("reply = %q, want the player list", got)
	}
	if n := server.authed.Load(); n != 1 {
		t.Errorf("authenticated %d times, want 1", n)
	}
}

// The connection is reused. Authenticating per command would triple the
// round trips on a screen that polls a roster.
func TestRCONReusesTheConnection(t *testing.T) {
	server := &fakeRCON{password: "secret", reply: "ok"}
	addr := server.start(t)

	client := &conn.RCON{Addr: addr, Password: "secret"}
	defer client.Close()

	for i := 0; i < 3; i++ {
		if _, err := client.Exec(context.Background(), "players"); err != nil {
			t.Fatalf("Exec %d: %v", i, err)
		}
	}
	if n := server.authed.Load(); n != 1 {
		t.Errorf("authenticated %d times for three commands, want 1", n)
	}
}

// A bad password has to be distinguishable from a server that is not
// listening: one is a settings problem and the other is a state problem.
func TestRCONReportsABadPassword(t *testing.T) {
	server := &fakeRCON{password: "secret", reply: "ok"}
	addr := server.start(t)

	client := &conn.RCON{Addr: addr, Password: "wrong"}
	defer client.Close()

	_, err := client.Exec(context.Background(), "players")
	if err == nil {
		t.Fatal("a wrong password succeeded")
	}
	if !errors.Is(err, conn.ErrAuth) {
		t.Errorf("error = %v, want it to wrap ErrAuth", err)
	}
}

// Some servers send an empty response packet before the auth verdict. The
// client has to read past it rather than treat it as the answer.
func TestRCONHandlesAChattyAuth(t *testing.T) {
	server := &fakeRCON{password: "secret", reply: "ok", authQuiet: true}
	addr := server.start(t)

	client := &conn.RCON{Addr: addr, Password: "secret"}
	defer client.Close()

	if _, err := client.Exec(context.Background(), "players"); err != nil {
		t.Fatalf("Exec: %v", err)
	}
}

// The common failure is a socket the server closed while nothing was using
// it — a restart, an idle timeout. One retry on a fresh connection means
// every capability does not have to handle that identically.
func TestRCONReconnectsAfterTheServerHangsUp(t *testing.T) {
	server := &fakeRCON{password: "secret", reply: "ok", closeAfter: 1}
	addr := server.start(t)

	client := &conn.RCON{Addr: addr, Password: "secret"}
	defer client.Close()

	if _, err := client.Exec(context.Background(), "first"); err != nil {
		t.Fatalf("first Exec: %v", err)
	}
	// The server hung up after that one. This must still work.
	if _, err := client.Exec(context.Background(), "second"); err != nil {
		t.Fatalf("second Exec after a hangup: %v", err)
	}
	if n := server.authed.Load(); n != 2 {
		t.Errorf("authenticated %d times, want 2 — one per connection", n)
	}
}

func TestRCONReportsAServerThatIsNotListening(t *testing.T) {
	// Port 1 on loopback: nothing is there and nothing will be.
	client := &conn.RCON{Addr: "127.0.0.1:1", Password: "secret"}
	defer client.Close()

	_, err := client.Exec(context.Background(), "players")
	if err == nil {
		t.Fatal("connecting to nothing succeeded")
	}
	if errors.Is(err, conn.ErrAuth) {
		t.Errorf("a closed port was reported as an auth failure: %v", err)
	}
	if !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("error = %v, want it to name the address", err)
	}
}

// A cancelled context is not a connection problem and must not be retried
// into a second doomed attempt.
func TestRCONRespectsACancelledContext(t *testing.T) {
	server := &fakeRCON{password: "secret", reply: "ok"}
	addr := server.start(t)

	client := &conn.RCON{Addr: addr, Password: "secret"}
	defer client.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := client.Exec(ctx, "players"); err == nil {
		t.Error("a cancelled context still ran the command")
	}
}

// A size field the server could not have meant is refused rather than
// allocated for: a game's log flood should not become an out-of-memory.
func TestRCONRefusesAnImplausiblePacket(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		readPacket(c) // the auth
		binary.Write(c, binary.LittleEndian, int32(1<<30))
		time.Sleep(50 * time.Millisecond)
	}()

	client := &conn.RCON{Addr: l.Addr().String(), Password: "secret"}
	defer client.Close()

	if _, err := client.Exec(context.Background(), "players"); err == nil {
		t.Error("an implausible packet size was accepted")
	}
}
