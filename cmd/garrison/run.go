package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/tasks"
)

// verbs are the actions available as subcommands.
//
// Every action in the TUI is also a subcommand, because the day you want one
// in Task Scheduler you will want it badly. They are peers over the same
// store rather than one wrapping the other: this file submits the same task
// the key press does, and waits for it.
var verbs = map[string]func(*core.Store, context.Context, string){
	"start":   func(s *core.Store, ctx context.Context, name string) { s.Start(ctx, name) },
	"stop":    func(s *core.Store, ctx context.Context, name string) { s.Stop(ctx, name) },
	"restart": func(s *core.Store, ctx context.Context, name string) { s.Restart(ctx, name) },
	"backup":  func(s *core.Store, ctx context.Context, name string) { s.Backup(ctx, name) },
	"update":  func(s *core.Store, ctx context.Context, name string) { s.Update(ctx, name) },
}

func verbNames() []string {
	out := make([]string, 0, len(verbs))
	for name := range verbs {
		out = append(out, name)
	}
	sortStrings(out)
	return out
}

// runVerb submits one task and waits for it to finish.
//
// Waiting is the point. A scheduled job that returns the moment the task is
// queued tells Task Scheduler the restart succeeded before the server has
// stopped, and the exit code is the only thing a scheduler reads.
func runVerb(name, confDir, endpoint string, interval, timeout time.Duration, args []string) error {
	submit, ok := verbs[name]
	if !ok {
		return fmt.Errorf("unknown command %q: try %s, status or version", name, strings.Join(verbNames(), ", "))
	}
	if len(args) == 0 {
		return fmt.Errorf("%s: which server? Usage: garrison %s <server>", name, name)
	}
	server := args[0]

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	store, wg, err := setup(ctx, confDir, endpoint, interval)
	if err != nil {
		return err
	}
	defer wg.Wait()
	defer cancel()

	sub := store.Subscribe()

	// Wait for the first poll before submitting, so "no such server" is
	// answered from a fleet that has actually been listed rather than from
	// an empty one.
	if err := awaitFleet(ctx, sub); err != nil {
		return err
	}
	if _, ok := store.Snapshot().Server(server); !ok {
		return fmt.Errorf("%s: no server called %q — garrison status lists them", name, server)
	}

	submit(store, ctx, server)
	return awaitTask(ctx, sub, server, name)
}

// awaitFleet blocks until the engine has answered once.
func awaitFleet(ctx context.Context, sub <-chan core.Snapshot) error {
	for {
		select {
		case <-ctx.Done():
			return errors.New("timed out waiting for the container engine")
		case snap, ok := <-sub:
			if !ok {
				return errors.New("store shut down before the first poll")
			}
			if snap.Engine.LastOK.IsZero() && snap.Engine.Err == "" {
				continue
			}
			if !snap.Engine.OK {
				return fmt.Errorf("%s: %s", snap.Engine.Transport, snap.Engine.Err)
			}
			return nil
		}
	}
}

// awaitTask follows the task for a server until it settles, printing each step
// as it starts so a scheduled run leaves a log worth reading.
func awaitTask(ctx context.Context, sub <-chan core.Snapshot, server, verb string) error {
	var lastStep string
	seen := false

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s %s: timed out", verb, server)
		case snap, ok := <-sub:
			if !ok {
				return fmt.Errorf("%s %s: store shut down before the task finished", verb, server)
			}

			task, found := latestTask(snap, server)
			if !found {
				if seen {
					// It was there and now is not, which the engine does
					// not do. Treat it as done rather than hanging.
					return nil
				}
				continue
			}
			seen = true

			if step := task.StepName(); step != "" && step != lastStep {
				lastStep = step
				fmt.Fprintf(os.Stderr, "%s: %s\n", server, step)
			}

			switch task.State {
			case tasks.StateDone:
				fmt.Printf("%s: %s done\n", server, verb)
				return nil
			case tasks.StateFailed, tasks.StateRolledBack:
				return fmt.Errorf("%s %s: %s", verb, server, task.Err)
			}
		}
	}
}

// latestTask is the newest task for a server.
func latestTask(snap core.Snapshot, server string) (tasks.Progress, bool) {
	var out tasks.Progress
	found := false
	for _, t := range snap.Tasks {
		if t.Server == server {
			out, found = t, true
		}
	}
	return out, found
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
