package metrics

import (
	"context"
	"time"

	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/services/streams"
)

// Observer receives samples. The store implements it.
//
// Declared here by the producer, like fleet.Observer, so this package never
// imports internal/core — see ADR 0007.
type Observer interface {
	StatsSampled(ctx context.Context, instance string, s host.Sample)
}

// Streamer keeps one stats stream per running container.
//
// The goroutine bookkeeping lives in internal/services/streams; what is left
// here is only what a stats stream does with its samples.
type Streamer struct {
	driver   host.Driver
	observer Observer
	sup      *streams.Supervisor

	// Now is the clock, injectable so a test asserts on a timestamp rather
	// than racing one.
	Now func() time.Time
}

// NewStreamer returns a streamer that is not yet running.
func NewStreamer(driver host.Driver, obs Observer) *Streamer {
	s := &Streamer{driver: driver, observer: obs}
	s.sup = streams.New(s.stream)
	return s
}

// Reconcile tells the streamer which containers exist now.
func (s *Streamer) Reconcile(containers []host.Container) { s.sup.Reconcile(containers) }

// Run owns every stream until ctx is cancelled. It blocks.
func (s *Streamer) Run(ctx context.Context) { s.sup.Run(ctx) }

// stream consumes one container's samples until the stream ends or ctx is
// cancelled. It does not reconnect — see the note on streams.Supervisor.
func (s *Streamer) stream(ctx context.Context, c host.Container) {
	samples, err := s.driver.Stats(ctx, c.ID)
	if err != nil {
		return
	}

	for {
		select {
		case <-ctx.Done():
			return
		case sample, ok := <-samples:
			if !ok {
				return
			}
			if sample.At.IsZero() {
				sample.At = s.now()
			}
			s.observer.StatsSampled(ctx, c.Instance, sample)
		}
	}
}

func (s *Streamer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
