package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tasks"
)

// Tasks is the engine, as the store needs it.
//
// An interface rather than the engine itself so the store can be tested with
// no engine at all, and so nothing here has to know how a task is run — only
// how to ask for one.
type Tasks interface {
	ID(kind tasks.Kind) string
	Submit(ctx context.Context, t *tasks.Task)
	Cancel(ctx context.Context, id string)
}

// Saver writes a server's configuration, for the apply task to hand on.
type Saver interface {
	Save(inst model.Instance) error
	Delete(name string) error
}

// Start brings a server up, creating its container if it has none.
func (s *Store) Start(ctx context.Context, instance string) {
	s.submit(ctx, instance, tasks.KindStart, func(id string) *tasks.Task {
		return tasks.Start(id, instance, tasks.TriggerManual)
	})
}

// Stop takes a server down with the signal and grace its game asks for.
func (s *Store) Stop(ctx context.Context, instance string) {
	s.submit(ctx, instance, tasks.KindStop, func(id string) *tasks.Task {
		return tasks.Stop(id, instance, tasks.TriggerManual)
	})
}

// Restart is stop and start as one unit, so a failure halfway puts the server
// back rather than leaving it down.
func (s *Store) Restart(ctx context.Context, instance string) {
	s.submit(ctx, instance, tasks.KindRestart, func(id string) *tasks.Task {
		return tasks.Restart(id, instance, tasks.TriggerManual)
	})
}

// ApplySettings writes a server's pending changes and does whatever they cost.
//
// recreate says whether the change needs a new container, which the caller
// works out from the game's Schema — the store does not know what a setting
// means, only that somebody decided this one is expensive.
func (s *Store) ApplySettings(ctx context.Context, instance string, recreate bool) {
	srv, ok := s.Snapshot().Server(instance)
	if !ok {
		s.raise(ctx, instance, fmt.Errorf("%s: apply: no such server", instance))
		return
	}
	if srv.Pending() == 0 {
		return
	}
	if s.saver == nil {
		s.raise(ctx, instance, fmt.Errorf("%s: apply: nowhere to write the configuration", instance))
		return
	}

	next := srv.Instance
	settings := make(map[string]any, len(next.Settings)+len(srv.Draft))
	for k, v := range next.Settings {
		settings[k] = v
	}
	for k, v := range srv.Draft {
		settings[k] = v
	}
	next.Settings = settings

	s.submit(ctx, instance, tasks.KindApplyConfig, func(id string) *tasks.Task {
		return tasks.ApplyConfig(id, instance, tasks.TriggerManual, next, s.saver, recreate)
	})
}

// EditSetting records a change the operator has made but not applied.
func (s *Store) EditSetting(ctx context.Context, instance, key string, value any) {
	s.Send(ctx, SettingEdited{At: s.now(), Server: instance, Key: key, Value: value})
}

// DiscardDraft throws away a server's unapplied changes.
func (s *Store) DiscardDraft(ctx context.Context, instance string) {
	s.Send(ctx, DraftDiscarded{At: s.now(), Server: instance})
}

// Backup archives a server's data directory.
func (s *Store) Backup(ctx context.Context, instance string) {
	if s.archives == nil {
		s.raise(ctx, instance, fmt.Errorf("%s: backup: nowhere to write archives", instance))
		return
	}
	s.submit(ctx, instance, tasks.KindBackup, func(id string) *tasks.Task {
		return tasks.Backup(id, instance, tasks.TriggerManual, s.archives.For(instance), s.keepBackups)
	})
}

// CreateServer writes a new server's configuration and adds it to the fleet.
//
// It does not start anything. Creating the container is what Start already
// does on a server that has none, so the wizard's job ends at a file on disk
// and a row in the fleet — and a server that appears configured but not built
// is a real, useful state rather than an incomplete one.
func (s *Store) CreateServer(ctx context.Context, inst model.Instance) {
	if s.saver == nil {
		s.raise(ctx, inst.Name, fmt.Errorf("%s: create: nowhere to write the configuration", inst.Name))
		return
	}
	if inst.Name == "" {
		s.raise(ctx, "", errors.New("create: a server needs a name"))
		return
	}
	if _, exists := s.Snapshot().Server(inst.Name); exists {
		s.raise(ctx, inst.Name, fmt.Errorf("%s: create: there is already a server with that name", inst.Name))
		return
	}
	if err := s.saver.Save(inst); err != nil {
		s.raise(ctx, inst.Name, err)
		return
	}
	s.Send(ctx, InstanceAdded{At: s.now(), Instance: inst})
}

// DeleteServer removes a server's container and configuration, and leaves its
// world where it is.
//
// The confirmation is the view's — it is one of the three actions that asks
// for the server's name typed out — but the restraint is here: nothing in this
// path touches the data directory, so the worst a mistaken delete costs is
// the configuration, which the task's compensation puts back.
func (s *Store) DeleteServer(ctx context.Context, instance string) {
	if s.saver == nil {
		s.raise(ctx, instance, fmt.Errorf("%s: delete: no configuration store", instance))
		return
	}
	inst, ok := s.Snapshot().Instance(instance)
	if !ok {
		s.raise(ctx, instance, fmt.Errorf("%s: delete: Garrison has no configuration for it, so there is nothing to remove", instance))
		return
	}
	s.submit(ctx, instance, tasks.KindDelete, func(id string) *tasks.Task {
		return tasks.Delete(id, instance, tasks.TriggerManual, s.saver, inst)
	})
}

// Restore replaces a server's world with an archive.
//
// The confirmation for this lives in the view — it is one of the actions that
// asks for the server's name to be typed — but the safety does not: the task
// archives what it is about to overwrite and unwinds to it if anything fails.
// A caller that skipped the prompt still cannot lose a world without a copy of
// it being taken first.
func (s *Store) Restore(ctx context.Context, instance, archive string) {
	if s.archives == nil {
		s.raise(ctx, instance, fmt.Errorf("%s: restore: nowhere to read archives from", instance))
		return
	}
	if archive == "" {
		s.raise(ctx, instance, fmt.Errorf("%s: restore: no archive chosen", instance))
		return
	}
	s.submit(ctx, instance, tasks.KindRestore, func(id string) *tasks.Task {
		return tasks.Restore(id, instance, tasks.TriggerManual, s.archives.For(instance), archive)
	})
}

// Update pulls the game's image and recreates the container, snapshotting the
// world first so a failure anywhere puts it back.
func (s *Store) Update(ctx context.Context, instance string) {
	if s.archives == nil {
		s.raise(ctx, instance, fmt.Errorf("%s: update: no backup directory, and an update without one is not offered", instance))
		return
	}
	s.submit(ctx, instance, tasks.KindUpdate, func(id string) *tasks.Task {
		return tasks.Update(id, instance, tasks.TriggerManual, s.archives.For(instance), s.keepBackups)
	})
}

// Players is how many are connected to a server, and whether that is known at
// all. It is the scheduler's window onto the fleet: not knowing is not the
// same as nobody being there.
func (s *Store) Players(server string) (int, bool) {
	srv, ok := s.Snapshot().Server(server)
	if !ok || !srv.State.Live() {
		return 0, false
	}
	return len(srv.Players), true
}

// SubmitScheduled queues a task the scheduler decided is due.
func (s *Store) SubmitScheduled(ctx context.Context, server string, kind tasks.Kind, trigger tasks.Trigger) {
	switch kind {
	case tasks.KindRestart:
		s.submit(ctx, server, kind, func(id string) *tasks.Task {
			return tasks.Restart(id, server, trigger)
		})
	case tasks.KindBackup:
		if s.archives == nil {
			return
		}
		s.submit(ctx, server, kind, func(id string) *tasks.Task {
			return tasks.Backup(id, server, trigger, s.archives.For(server), s.keepBackups)
		})
	case tasks.KindUpdate:
		if s.archives == nil {
			return
		}
		s.submit(ctx, server, kind, func(id string) *tasks.Task {
			return tasks.Update(id, server, trigger, s.archives.For(server), s.keepBackups)
		})
	default:
		s.raise(ctx, server, fmt.Errorf("%s: %s cannot be scheduled yet", server, kind))
	}
}

// Notify reports something the scheduler decided, which is how a deferred job
// reaches the operator instead of only the log.
func (s *Store) Notify(ctx context.Context, server, text string) {
	s.Send(ctx, NoticeRaised{At: s.now(), Level: LevelWarn, Server: server, Text: text})
}

// CancelTask asks the engine to stop one. It compensates rather than simply
// stopping, so the world is left where it started.
func (s *Store) CancelTask(ctx context.Context, id string) {
	if engine := s.engine(); engine != nil {
		engine.Cancel(ctx, id)
	}
}

// submit refuses what cannot succeed before queueing anything, so the answer
// is a sentence on the status bar rather than a task that fails two seconds
// later for a reason the operator could have been told immediately.
func (s *Store) submit(ctx context.Context, instance string, kind tasks.Kind, build func(id string) *tasks.Task) {
	snap := s.Snapshot()

	srv, ok := snap.Server(instance)
	if !ok {
		s.raise(ctx, instance, fmt.Errorf("%s: %s: no such server", instance, kind))
		return
	}
	if reason, ok := refuse(srv, kind); !ok {
		s.raise(ctx, instance, fmt.Errorf("%s: %s: %s", instance, kind, reason))
		return
	}
	engine := s.engine()
	if engine == nil {
		s.raise(ctx, instance, fmt.Errorf("%s: %s: no task engine attached", instance, kind))
		return
	}

	engine.Submit(ctx, build(engine.ID(kind)))
}

// refuse rejects the operations that cannot succeed.
//
// Unknown state is not refused: if the engine is unreachable the state is
// stale, and the honest response to "start it anyway" is to try and report
// what happens.
func refuse(srv Server, kind tasks.Kind) (string, bool) {
	switch {
	case kind == tasks.KindStart && srv.State == model.StateRunning:
		return "already running", false
	case kind == tasks.KindStop && srv.State == model.StateStopped && srv.Created:
		return "already stopped", false
	case kind == tasks.KindStop && !srv.Created:
		return "it has never been created", false
	}
	return "", true
}

func (s *Store) raise(ctx context.Context, instance string, err error) {
	s.Send(ctx, NoticeRaised{At: s.now(), Level: LevelError, Server: instance, Text: err.Error()})
}
