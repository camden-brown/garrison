package core

import (
	"context"
	"fmt"

	"github.com/camden-brown/garrison/internal/model"
)

// Control is the side of the world that can change a container.
//
// It is an interface rather than a host.Driver so the store never does I/O
// itself: the implementation lives in internal/services/fleet, below the
// store, and the store is left as what it should be — a reducer with a
// mailbox. It also means every store test runs with no runtime at all.
type Control interface {
	Start(ctx context.Context, instance, id string) error
	Stop(ctx context.Context, instance, id string) error
}

// Start brings a server up. It returns as soon as the operation is recorded;
// the work happens in the background and reports back as mutations.
//
// At M2 this becomes a task with steps, a durable record and a declared
// compensation, which is where anything slower than a frame belongs. It is a
// bare goroutine here because M0 has no task engine yet and a fleet list you
// cannot start a server from is not worth looking at. The shape is the same
// one a task will emit: began, then ended with or without an error.
func (s *Store) Start(ctx context.Context, instance string) {
	s.operate(ctx, instance, OpStart, func(ctl Control, id string) error {
		return ctl.Start(ctx, instance, id)
	})
}

// Stop takes a server down with the game's own signal and grace period.
func (s *Store) Stop(ctx context.Context, instance string) {
	s.operate(ctx, instance, OpStop, func(ctl Control, id string) error {
		return ctl.Stop(ctx, instance, id)
	})
}

func (s *Store) operate(ctx context.Context, instance string, op Op, run func(Control, string) error) {
	srv, ok := s.Snapshot().Server(instance)
	if !ok {
		s.raise(ctx, instance, fmt.Errorf("%s: %s: no such server", instance, op))
		return
	}
	if srv.Busy != OpNone {
		s.raise(ctx, instance, fmt.Errorf("%s: %s: already %s", instance, op, srv.Busy.Present()))
		return
	}
	if reason, ok := refuse(srv, op); !ok {
		s.raise(ctx, instance, fmt.Errorf("%s: %s: %s", instance, op, reason))
		return
	}
	if s.ctl == nil {
		s.raise(ctx, instance, fmt.Errorf("%s: %s: no container runtime attached", instance, op))
		return
	}

	s.Send(ctx, OperationBegan{At: s.now(), Server: instance, Op: op})
	ctl, id := s.ctl, srv.ID
	go func() {
		err := run(ctl, id)
		s.Send(ctx, OperationEnded{At: s.now(), Server: instance, Op: op, Err: err})
	}()
}

// refuse rejects the operations that cannot succeed, so the answer is a
// sentence on the status bar rather than a Docker error two seconds later.
//
// Unknown is not refused: if the engine is unreachable the state is stale, and
// the honest response to "start it anyway" is to try and report what happens.
func refuse(srv Server, op Op) (string, bool) {
	switch {
	case op == OpStart && srv.State == model.StateRunning:
		return "already running", false
	case op == OpStop && srv.State == model.StateStopped:
		return "already stopped", false
	}
	return "", true
}

func (s *Store) raise(ctx context.Context, instance string, err error) {
	s.Send(ctx, NoticeRaised{At: s.now(), Level: LevelError, Server: instance, Text: err.Error()})
}
