package core

import (
	"context"
	"time"

	"github.com/camden-brown/garrison/internal/host"
)

// The methods below make a *Store an observer for the pollers in
// internal/services. They are the seam that lets a service produce state
// without importing the store: a service declares the observer interface it
// needs, cmd/garrison hands it the store, and neither package knows the other
// exists.
//
// They are thin on purpose. Everything interesting is in the reducer.

// FleetObserved records a complete poll of the engine.
func (s *Store) FleetObserved(ctx context.Context, at time.Time, containers []host.Container) {
	s.Send(ctx, FleetObserved{At: at, Containers: containers})
}

// FleetUnobservable records the engine failing to answer.
func (s *Store) FleetUnobservable(ctx context.Context, at time.Time, err error) {
	s.Send(ctx, FleetUnobservable{At: at, Err: err})
}
