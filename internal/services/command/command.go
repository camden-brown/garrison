// Package command runs console commands against a server and reports what it
// said.
//
// This is deliberately not a task, and the rule it appears to break is worth
// naming: "slower than one frame is a task" exists so that anything which
// changes the world durably is journalled, compensable and watchable. A
// console command is none of those things — it is a question, its answer
// belongs on the screen you asked it from, and putting a task lane entry
// beside every chat message would make the Tasks view useless for the things
// it is for.
//
// So it is a service, like the log and stats streamers: it does I/O off the
// render loop, and what comes back arrives as an event in the console ring
// where it would have appeared anyway if the server had said it unprompted.
package command

import (
	"context"
	"strings"
	"time"

	"github.com/camden-brown/garrison/internal/model"
)

// Request is one command to run.
type Request struct {
	Server string
	Text   string
}

// Runner is the capability, already bound to a transport. cmd adapts a
// games.Commandable into this, for the same reason it does for a drain: this
// package must not import internal/games.
type Runner interface {
	// Run sends a command and returns what the server said. The second
	// return says whether this server has a command channel at all, which
	// is a different answer from a command that failed.
	Run(ctx context.Context, server, cmd string) (reply string, ok bool, err error)
}

// Observer receives the reply as console output.
type Observer interface {
	LogEventsRead(ctx context.Context, server string, events []model.Event)
}

// Service runs commands one at a time.
//
// Serialised on purpose. The transport underneath is a single RCON socket per
// server and the game behind it is single-threaded, so two commands in flight
// buys nothing and costs the ability to say which reply belongs to which
// question. A queue of one is enough for a person typing.
type Service struct {
	Runner Runner

	// Queue is where requests arrive. Buffered so a burst of typing does
	// not block the shell's update loop, and bounded so a wedged server
	// cannot grow it without limit.
	Queue chan Request
}

// New returns a service with a queue sized for a person rather than a script.
func New(r Runner) *Service {
	return &Service{Runner: r, Queue: make(chan Request, 16)}
}

// Send queues a command, dropping it if the queue is full rather than blocking
// the caller.
//
// Dropping is reported to the caller so the console can say so. A command
// silently discarded is worse than one refused: the operator would retype it,
// and on a server that is merely slow they would then run it twice.
func (s *Service) Send(server, text string) bool {
	if s == nil || s.Queue == nil {
		return false
	}
	select {
	case s.Queue <- Request{Server: server, Text: text}:
		return true
	default:
		return false
	}
}

// Run serves the queue until the context is cancelled.
func (s *Service) Run(ctx context.Context, obs Observer) {
	if s == nil || s.Queue == nil || obs == nil {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case req := <-s.Queue:
			s.one(ctx, obs, req)
		}
	}
}

func (s *Service) one(ctx context.Context, obs Observer, req Request) {
	cmd := strings.TrimSpace(req.Text)
	if cmd == "" {
		return
	}

	// The command is echoed before it is sent. A console that shows only
	// replies leaves you unsure whether the thing you typed went anywhere,
	// and on a server that takes a while to answer that is the moment
	// somebody sends it again.
	obs.LogEventsRead(ctx, req.Server, []model.Event{{
		Kind: model.KindAdmin,
		At:   time.Now(),
		Text: "> " + cmd,
	}})

	if s.Runner == nil {
		s.say(ctx, obs, req.Server, model.KindError, "no command runner is wired")
		return
	}

	reply, ok, err := s.Runner.Run(ctx, req.Server, cmd)
	switch {
	case !ok:
		s.say(ctx, obs, req.Server, model.KindError,
			"this server has no command channel")
	case err != nil:
		s.say(ctx, obs, req.Server, model.KindError, err.Error())
	default:
		for _, line := range strings.Split(strings.TrimRight(reply, "\n"), "\n") {
			s.say(ctx, obs, req.Server, model.KindInfo, line)
		}
	}
}

func (s *Service) say(ctx context.Context, obs Observer, server string, kind model.Kind, text string) {
	obs.LogEventsRead(ctx, server, []model.Event{{
		Kind: kind, At: time.Now(), Text: text,
	}})
}
