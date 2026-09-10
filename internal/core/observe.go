package core

import (
	"context"
	"time"

	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tasks"
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
	s.Send(ctx, FleetObserved{At: at, Containers: containers, MetricLabels: s.metricLabels})
}

// FleetUnobservable records the engine failing to answer.
func (s *Store) FleetUnobservable(ctx context.Context, at time.Time, err error) {
	s.Send(ctx, FleetUnobservable{At: at, Err: err})
}

// LogEventsRead records a batch of parsed log lines.
func (s *Store) LogEventsRead(ctx context.Context, instance string, events []model.Event) {
	s.Send(ctx, LogEventsRead{At: s.now(), Server: instance, Events: events})
}

// TaskProgressed records where a task got to.
func (s *Store) TaskProgressed(ctx context.Context, p tasks.Progress) {
	s.Send(ctx, TaskProgressed{At: s.now(), Progress: p})
}

// InstancesLoaded records what the config directory says.
func (s *Store) InstancesLoaded(ctx context.Context, instances []model.Instance) {
	s.Send(ctx, InstancesLoaded{At: s.now(), Instances: instances})
}

// HostDescribed records the engine's own figures.
func (s *Store) HostDescribed(ctx context.Context, at time.Time, info host.Info) {
	s.Send(ctx, HostDescribed{At: at, Info: info})
}

// StatsSampled records one point from a container's stats stream.
func (s *Store) StatsSampled(ctx context.Context, instance string, sample host.Sample) {
	s.Send(ctx, StatsSampled{At: s.now(), Server: instance, Sample: sample})
}

// BackupsListed records what the archive poller found for one server.
func (s *Store) BackupsListed(ctx context.Context, at time.Time, server string, archives []model.Archive) {
	s.Send(ctx, BackupsListed{At: at, Server: server, Archive: archives})
}

// SessionsListed records the player history the tracker read back.
func (s *Store) SessionsListed(ctx context.Context, at time.Time, server string, sessions []model.Session) {
	s.Send(ctx, SessionsListed{At: at, Server: server, Sessions: sessions})
}

// RosterObserved records who a server says is connected.
func (s *Store) RosterObserved(ctx context.Context, at time.Time, server string, players []model.Player) {
	s.Send(ctx, RosterObserved{At: at, Server: server, Players: players})
}
