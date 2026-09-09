package tasks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/camden-brown/garrison/internal/model"
)

// Saver writes a server's configuration. Declared here so this package does
// not import internal/config; config.Store satisfies it.
type Saver interface {
	Save(inst model.Instance) error
}

// Stop behaviour when the plan does not say. A game's Plan supplies both and
// they are game facts — Valheim traps SIGINT to save, Zomboid needs two
// minutes — so this is only for a server with no plugin.
const (
	fallbackStopSignal = "SIGTERM"
	fallbackStopGrace  = 60 * time.Second
	healthBudget       = 90 * time.Second
)

// Restart takes a server down and brings it back.
//
// It is a subset of the update composition rather than a different thing: the
// same stop, the same start, the same healthcheck, without the parts that
// change what is on disk. Keeping them the same shape is what makes both
// behave identically when they fail halfway.
func Restart(id, server string, trigger Trigger) *Task {
	return &Task{
		ID: id, Server: server, Kind: KindRestart, Trigger: trigger,
		Steps: []Step{stopStep(), startStep(), healthStep()},
	}
}

// Start brings a server up, creating the container first if there is none.
func Start(id, server string, trigger Trigger) *Task {
	return &Task{
		ID: id, Server: server, Kind: KindStart, Trigger: trigger,
		Steps: []Step{ensureStep(), startStep(), healthStep()},
	}
}

// Stop takes a server down.
func Stop(id, server string, trigger Trigger) *Task {
	return &Task{
		ID: id, Server: server, Kind: KindStop, Trigger: trigger,
		Steps: []Step{stopStep()},
	}
}

// ApplyConfig writes changed settings and does whatever they cost.
//
// The impact decides the shape: a change that only touches a config file is
// written and that is all, while one that changes the container's environment
// or ports needs a new container, because those are fixed when it is created.
// The caller works out which from the game's Schema; this builds the sequence
// for the answer.
func ApplyConfig(id, server string, trigger Trigger, next model.Instance, save Saver, recreate bool) *Task {
	steps := []Step{writeConfigStep(next, save), compileStep()}
	if recreate {
		steps = append(steps, stopStep(), removeStep(), createStep(), startStep(), healthStep())
	}
	return &Task{ID: id, Server: server, Kind: KindApplyConfig, Trigger: trigger, Steps: steps}
}

// stopStep asks the server to leave, with the signal and grace its game wants.
func stopStep() Step {
	return Step{
		Name: "stop",
		Est:  30 * time.Second,
		Run: func(ctx context.Context, s *StepCtx) error {
			id, err := containerID(ctx, s)
			if err != nil {
				return err
			}
			if id == "" {
				s.Say("already down")
				return nil
			}

			signal, grace := stopBehaviour(s)
			// The grace is a game fact read from its plan, so the estimate
			// only becomes real here. Without this the row says "stopping…"
			// with no budget, and sixty seconds of that is how an operator
			// concludes it has hung.
			s.Estimate(grace)
			s.Set("was-running", true)
			s.Say(fmt.Sprintf("stopping with %s, up to %s", signal, grace))
			return s.Driver.Stop(ctx, id, signal, grace)
		},
		// Undo starts it again. A stop that has to be taken back means a
		// later step failed, and the server was up before we touched it.
		Undo: func(ctx context.Context, s *StepCtx) error {
			if was, _ := s.Values["was-running"].(bool); !was {
				return nil
			}
			id, err := containerID(ctx, s)
			if err != nil || id == "" {
				return err
			}
			return s.Driver.Start(ctx, id)
		},
	}
}

func startStep() Step {
	return Step{
		Name: "start",
		Est:  10 * time.Second,
		Run: func(ctx context.Context, s *StepCtx) error {
			id, err := containerID(ctx, s)
			if err != nil {
				return err
			}
			if id == "" {
				return errors.New("no container to start")
			}
			return s.Driver.Start(ctx, id)
		},
		// No Undo: the compensation for starting is stopping, and the stop
		// step below it already puts the server back where it was. Undoing
		// here as well would stop it twice.
	}
}

// ensureStep creates the container when there is not one, so starting a
// configured-but-never-created server does the obvious thing.
func ensureStep() Step {
	return Step{
		Name: "ensure container",
		Est:  5 * time.Second,
		Run: func(ctx context.Context, s *StepCtx) error {
			id, err := containerID(ctx, s)
			if err != nil {
				return err
			}
			if id != "" {
				return nil
			}
			return createContainer(ctx, s)
		},
		Undo: func(ctx context.Context, s *StepCtx) error {
			created, _ := s.Values["created"].(string)
			if created == "" {
				return nil
			}
			return s.Driver.Remove(ctx, created, false)
		},
	}
}

func createStep() Step {
	return Step{
		Name: "recreate container",
		Est:  5 * time.Second,
		Run:  createContainer,
		Undo: func(ctx context.Context, s *StepCtx) error {
			created, _ := s.Values["created"].(string)
			if created == "" {
				return nil
			}
			return s.Driver.Remove(ctx, created, false)
		},
	}
}

// removeStep destroys the container. The volume is untouched — that is the
// whole reason recreating is safe, and why this step has no compensation of
// its own: the create that follows is the compensation.
func removeStep() Step {
	return Step{
		Name: "remove container",
		Est:  3 * time.Second,
		Run: func(ctx context.Context, s *StepCtx) error {
			id, err := containerID(ctx, s)
			if err != nil || id == "" {
				return err
			}
			s.Say("removing the container; the data volume is untouched")
			return s.Driver.Remove(ctx, id, false)
		},
	}
}

func healthStep() Step {
	return Step{
		Name: "healthcheck",
		Est:  healthBudget,
		Run: func(ctx context.Context, s *StepCtx) error {
			deadline := time.Now().Add(healthBudget)
			for {
				id, err := containerID(ctx, s)
				if err != nil {
					return err
				}
				c, err := s.Driver.Inspect(ctx, id)
				if err != nil {
					return err
				}

				switch {
				case c.State == model.StateCrashed:
					return fmt.Errorf("exited during startup: %s", c.Detail)
				case c.State.Live() && c.Health.OK:
					s.Say("healthy")
					return nil
				}

				if time.Now().After(deadline) {
					// Not healthy inside the budget is a failure, so the
					// rollback runs. A server that comes up unhealthy and
					// stays that way is worse than one that never came up,
					// because nobody is watching by then.
					return fmt.Errorf("not healthy within %s", healthBudget)
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(2 * time.Second):
				}
			}
		},
	}
}

// writeConfigStep saves the server file, remembering the previous one so it
// can be put back.
func writeConfigStep(next model.Instance, save Saver) Step {
	return Step{
		Name: "write config",
		Est:  time.Second,
		Run: func(ctx context.Context, s *StepCtx) error {
			s.Set("previous-instance", s.Instance)
			if err := save.Save(next); err != nil {
				return err
			}
			s.Instance = next
			s.Say("settings written")
			return nil
		},
		Undo: func(ctx context.Context, s *StepCtx) error {
			prev, ok := s.Values["previous-instance"].(model.Instance)
			if !ok {
				return nil
			}
			s.Instance = prev
			return save.Save(prev)
		},
	}
}

// compileStep turns settings into the files the game reads.
//
// Valheim returns none — it is configured entirely by environment — and that
// is not a special case here: an empty result writes nothing and the step
// succeeds, which is exactly what ADR 0006 wanted the interface to handle
// honestly from the first game.
func compileStep() Step {
	return Step{
		Name: "compile config files",
		Est:  time.Second,
		Run: func(ctx context.Context, s *StepCtx) error {
			compiler, ok := s.Game.(interface {
				Compile(model.Instance) ([]model.File, error)
			})
			if !ok {
				return nil
			}
			files, err := compiler.Compile(s.Instance)
			if err != nil {
				return err
			}
			if len(files) == 0 {
				s.Say("no config files for this game")
				return nil
			}
			s.Say(fmt.Sprintf("%d config file(s) to write", len(files)))
			s.Set("files", files)
			return nil
		},
	}
}

func createContainer(ctx context.Context, s *StepCtx) error {
	plan, err := s.Game.Plan(s.Instance)
	if err != nil {
		return err
	}
	id, err := s.Driver.Create(ctx, s.Instance, plan)
	if err != nil {
		return err
	}
	s.Set("created", id)
	s.Say("created " + id[:min(12, len(id))])
	return nil
}

// containerID finds the server's container, or empty when there is none.
func containerID(ctx context.Context, s *StepCtx) (string, error) {
	if id, ok := s.String("created"); ok && id != "" {
		return id, nil
	}

	all, err := s.Driver.List(ctx)
	if err != nil {
		return "", err
	}
	for _, c := range all {
		if c.Instance == s.Instance.Name {
			return c.ID, nil
		}
	}
	return "", nil
}

func stopBehaviour(s *StepCtx) (string, time.Duration) {
	signal, grace := fallbackStopSignal, fallbackStopGrace
	if s.Game == nil {
		return signal, grace
	}
	plan, err := s.Game.Plan(s.Instance)
	if err != nil {
		return signal, grace
	}
	if plan.StopSignal != "" {
		signal = plan.StopSignal
	}
	if plan.StopGrace > 0 {
		grace = plan.StopGrace
	}
	return signal, grace
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
