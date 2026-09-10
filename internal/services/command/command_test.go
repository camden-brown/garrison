package command_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/services/command"
)

type stubRunner struct {
	reply string
	ok    bool
	err   error

	mu   sync.Mutex
	sent []string
}

func (s *stubRunner) Run(_ context.Context, server, cmd string) (string, bool, error) {
	s.mu.Lock()
	s.sent = append(s.sent, server+": "+cmd)
	s.mu.Unlock()
	return s.reply, s.ok, s.err
}

func (s *stubRunner) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sent...)
}

type collector struct {
	mu     sync.Mutex
	events []model.Event
	got    chan struct{}
}

func newCollector() *collector {
	return &collector{got: make(chan struct{}, 64)}
}

func (c *collector) LogEventsRead(_ context.Context, _ string, events []model.Event) {
	c.mu.Lock()
	c.events = append(c.events, events...)
	c.mu.Unlock()
	select {
	case c.got <- struct{}{}:
	default:
	}
}

func (c *collector) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for _, e := range c.events {
		b.WriteString(e.Text)
		b.WriteString("\n")
	}
	return b.String()
}

func (c *collector) waitFor(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		c.mu.Lock()
		have := len(c.events)
		c.mu.Unlock()
		if have >= n {
			return
		}
		select {
		case <-c.got:
		case <-deadline:
			t.Fatalf("only %d events arrived, wanted %d:\n%s", have, n, c.text())
		}
	}
}

func run(t *testing.T, r command.Runner) (*command.Service, *collector) {
	t.Helper()
	svc := command.New(r)
	c := newCollector()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go svc.Run(ctx, c)
	return svc, c
}

// The reply lands in the console, which is where it would have appeared if
// the server had said it unprompted — and is the reason this is a service
// rather than a task.
func TestTheReplyArrivesAsConsoleOutput(t *testing.T) {
	runner := &stubRunner{reply: "Players connected (1):\n-Huldra", ok: true}
	svc, out := run(t, runner)

	if !svc.Send("zomboid-main", "players") {
		t.Fatal("the command was not queued")
	}
	out.waitFor(t, 3) // the echo plus two reply lines

	got := out.text()
	if !strings.Contains(got, "> players") {
		t.Errorf("the command was not echoed:\n%s", got)
	}
	if !strings.Contains(got, "-Huldra") {
		t.Errorf("the reply is missing:\n%s", got)
	}
	if calls := runner.calls(); len(calls) != 1 || calls[0] != "zomboid-main: players" {
		t.Errorf("runner saw %v", calls)
	}
}

// A console that shows only replies leaves you unsure whether what you typed
// went anywhere, and on a slow server that is when somebody sends it twice.
func TestTheCommandIsEchoedBeforeItIsSent(t *testing.T) {
	runner := &stubRunner{reply: "ok", ok: true}
	svc, out := run(t, runner)

	svc.Send("zomboid-main", "save")
	out.waitFor(t, 2)

	out.mu.Lock()
	first := out.events[0]
	out.mu.Unlock()

	if !strings.HasPrefix(first.Text, "> ") {
		t.Errorf("the first event is %q, want the echo", first.Text)
	}
	if first.Kind != model.KindAdmin {
		t.Errorf("the echo is %v, want admin", first.Kind)
	}
}

// A game with no channel is a different answer from a command that failed,
// and the console has to say which.
func TestAServerWithNoChannelSaysSo(t *testing.T) {
	svc, out := run(t, &stubRunner{ok: false})

	svc.Send("valheim-main", "players")
	out.waitFor(t, 2)

	got := out.text()
	if !strings.Contains(got, "no command channel") {
		t.Errorf("output does not explain the missing channel:\n%s", got)
	}
}

func TestAFailedCommandReportsWhy(t *testing.T) {
	svc, out := run(t, &stubRunner{ok: true, err: errors.New("connection refused")})

	svc.Send("zomboid-main", "players")
	out.waitFor(t, 2)

	got := out.text()
	if !strings.Contains(got, "connection refused") {
		t.Errorf("output does not carry the cause:\n%s", got)
	}
}

// A command silently discarded is worse than one refused: the operator would
// retype it, and on a server that is merely slow they would run it twice.
func TestAFullQueueRefusesRatherThanBlocking(t *testing.T) {
	// No Run goroutine, so nothing drains the queue.
	svc := command.New(&stubRunner{ok: true})

	var refused bool
	for i := 0; i < cap(svc.Queue)+5; i++ {
		if !svc.Send("zomboid-main", "players") {
			refused = true
			break
		}
	}
	if !refused {
		t.Error("the queue accepted more than it can hold")
	}
}

func TestAnEmptyCommandIsNotSent(t *testing.T) {
	runner := &stubRunner{ok: true, reply: "ok"}
	svc, _ := run(t, runner)

	svc.Send("zomboid-main", "   ")
	// Give the service a moment to have not done anything.
	time.Sleep(50 * time.Millisecond)

	if calls := runner.calls(); len(calls) != 0 {
		t.Errorf("an empty command was sent: %v", calls)
	}
}

func TestNothingWiredIsANoOp(t *testing.T) {
	done := make(chan struct{})
	go func() {
		(&command.Service{}).Run(context.Background(), nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a service with nothing wired did not return")
	}

	if (&command.Service{}).Send("a", "b") {
		t.Error("a service with no queue accepted a command")
	}
}
