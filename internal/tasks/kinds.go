package tasks

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/camden-brown/garrison/internal/model"
)

// Saver writes a server's configuration. Declared here so this package does
// not import internal/config; config.Store satisfies it.
type Saver interface {
	Save(inst model.Instance) error
	// Delete removes a server's configuration file.
	Delete(name string) error
}

// Delete takes a server out of Garrison: the container goes, the
// configuration goes, and the world stays.
//
// Not deleting the data is the whole shape of it. DESIGN puts this among the
// three actions that ask for the server's name typed out, and even behind that
// prompt a key that could erase a world nobody has a backup of is a key with
// no business existing. What this removes is everything Garrison made; what it
// leaves is the one thing it did not.
//
// The configuration is deleted last and its compensation writes it back, so a
// delete that fails at the container leaves a server Garrison still knows
// about rather than an orphaned directory and no record of what it was.
func Delete(id, server string, trigger Trigger, save Saver, inst model.Instance) *Task {
	return &Task{
		ID: id, Server: server, Kind: KindDelete, Trigger: trigger,
		Steps: []Step{stopStep(), removeStep(), forgetStep(save, inst)},
	}
}

func forgetStep(save Saver, inst model.Instance) Step {
	return Step{
		Name: "forget the configuration",
		Est:  time.Second,
		Run: func(ctx context.Context, s *StepCtx) error {
			if save == nil {
				return errors.New("no configuration store, so there is nothing to forget")
			}
			if err := save.Delete(inst.Name); err != nil {
				return err
			}
			s.Say("removed the server's configuration; " + describeData(inst))
			return nil
		},
		Undo: func(ctx context.Context, s *StepCtx) error {
			if save == nil {
				return nil
			}
			return save.Save(inst)
		},
	}
}

// describeData says where the world was left, because the value of this
// command is as much what it did not do as what it did.
func describeData(inst model.Instance) string {
	if inst.Data == "" {
		return "there was no data directory"
	}
	return "the world is still at " + inst.Data
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
	return RestartWithDrain(id, server, trigger, 0)
}

// DrainWarnings are when players are told, counting down to the stop.
//
// DESIGN's figures. They are spaced the way somebody actually reacts: a
// quarter of an hour to finish what you are doing, five minutes to get
// somewhere safe, one minute to stop moving.
var DrainWarnings = []time.Duration{15 * time.Minute, 5 * time.Minute, time.Minute}

// RestartWithDrain warns players, waits, saves, and then restarts.
//
// A drain of zero is a restart, which is what the plain Restart is. That is
// the whole difference: a scheduled nightly bounce wants fifteen minutes of
// warning and an operator fixing a wedged server wants none, and both are the
// same sequence with a different first step.
//
// The warning is a capability, so a game with no channel to speak to its
// players on degrades to a restart that does not warn them — and says so in
// the task's own history rather than silently skipping.
func RestartWithDrain(id, server string, trigger Trigger, drain time.Duration) *Task {
	steps := []Step{}
	if drain > 0 {
		steps = append(steps, drainStep(drain))
	}
	steps = append(steps, stopStep(), startStep(), healthStep())

	return &Task{
		ID: id, Server: server, Kind: KindRestart, Trigger: trigger,
		Steps: steps,
	}
}

// drainStep warns and waits.
//
// It has no compensation and needs none: it changes nothing. Nothing it does
// is undoable because nothing it does is a change — the world is exactly as it
// was, and the players have been told something that turned out not to happen,
// which is a disappointment rather than damage.
func drainStep(drain time.Duration) Step {
	return Step{
		Name: "drain",
		Est:  drain,
		Run: func(ctx context.Context, s *StepCtx) error {
			if s.Drain == nil {
				// Either the game has no channel or the server is not
				// reachable. Waiting out the drain anyway would be a delay
				// that helps nobody, so it is skipped and named.
				s.Say("no channel to warn players on; restarting without a drain")
				return nil
			}

			deadline := time.Now().Add(drain)
			for _, at := range DrainWarnings {
				if at > drain {
					// A five-minute drain does not announce fifteen.
					continue
				}
				wait := time.Until(deadline.Add(-at))
				if wait > 0 {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(wait):
					}
				}
				if err := s.Drain.Warn(ctx, at); err != nil {
					// A warning that did not send is worth saying and not
					// worth failing for: the restart is still the right
					// thing to do and the alternative is a server nobody
					// can restart because its chat is broken.
					s.Say("could not warn players: " + err.Error())
					break
				}
				s.Say("warned players: " + at.String())
			}

			if wait := time.Until(deadline); wait > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(wait):
				}
			}

			// Save before the stop, so the world on disk is one a player
			// would recognise rather than whatever the last autosave caught.
			if err := s.Drain.Save(ctx); err != nil {
				s.Say("could not ask the server to save: " + err.Error())
			} else {
				s.Say("world saved")
			}
			return nil
		},
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

// Archiver takes, prunes and puts back backups. Declared here so this package
// does not import internal/services/backup.
type Archiver interface {
	Create(ctx context.Context, src string, at time.Time) (path string, bytes int64, err error)
	Prune(keep int) ([]string, error)
	// Restore unpacks an archive over dst, replacing what is there.
	Restore(ctx context.Context, archive, dst string) error
}

// Restore replaces a server's world with an archive.
//
// This is the most destructive thing Garrison does, and the shape of the task
// is the argument for why it is safe to offer at all. The world being
// overwritten is archived first, and that archive is the compensation for the
// step that overwrites it: if the restore fails halfway, or the server will
// not come back up afterwards, the engine unwinds and puts back exactly what
// was there. A restore that cannot be undone is a restore nobody should run
// against a world they care about, which is most of them.
//
// The stop comes first because unpacking a world under a running server
// produces a corrupt one, and the game would keep writing over what was
// restored.
func Restore(id, server string, trigger Trigger, archive Archiver, from string) *Task {
	return &Task{
		ID: id, Server: server, Kind: KindRestore, Trigger: trigger,
		Steps: []Step{
			stopStep(),
			safetySnapshotStep(archive),
			restoreStep(archive, from),
			startStep(),
			healthStep(),
		},
	}
}

// safetySnapshotStep archives the world that is about to be replaced.
//
// It is not the same as the Backup task's snapshot: it is never pruned, it is
// taken with the server already stopped so it is consistent rather than hot,
// and its only purpose is to be the thing restoreStep undoes to.
func safetySnapshotStep(archive Archiver) Step {
	return Step{
		Name: "archive the world being replaced",
		Est:  30 * time.Second,
		Run: func(ctx context.Context, s *StepCtx) error {
			if s.Instance.Data == "" {
				return errors.New("no data directory configured, so there is nothing to archive")
			}
			path, size, err := archive.Create(ctx, s.Instance.Data, time.Now())
			if err != nil {
				return fmt.Errorf("archiving the current world before replacing it: %w", err)
			}
			s.Set("safety", path)
			s.Say(fmt.Sprintf("kept %s of the world being replaced", humanBytes(size)))
			return nil
		},
	}
}

func restoreStep(archive Archiver, from string) Step {
	return Step{
		Name: "restore archive",
		Est:  60 * time.Second,
		Run: func(ctx context.Context, s *StepCtx) error {
			if from == "" {
				return errors.New("no archive named to restore from")
			}
			if s.Instance.Data == "" {
				return errors.New("no data directory configured, so there is nowhere to restore to")
			}
			if err := archive.Restore(ctx, from, s.Instance.Data); err != nil {
				return err
			}
			s.Say("restored " + filepath.Base(from))
			return nil
		},
		Undo: func(ctx context.Context, s *StepCtx) error {
			safety, _ := s.String("safety")
			if safety == "" {
				// The one step where a silent no-op would be a lie. If the
				// safety archive is missing the world is whatever the failed
				// restore left, and saying so is the only useful thing left.
				return errors.New("no safety archive was taken, so the previous world cannot be put back")
			}
			return archive.Restore(ctx, safety, s.Instance.Data)
		},
	}
}

// Backup archives a server's data directory.
//
// A hot copy by default: DESIGN offers Quiesce for games that can pause their
// writes, and a game that cannot gets an archive taken while it plays. That is
// the honest trade — a slightly inconsistent backup you have beats a
// consistent one you skipped because it needed downtime.
func Backup(id, server string, trigger Trigger, archive Archiver, keep int) *Task {
	return &Task{
		ID: id, Server: server, Kind: KindBackup, Trigger: trigger,
		Steps: []Step{quiesceStep(), snapshotStep(archive), pruneStep(archive, keep)},
	}
}

// quiesceStep pauses writes for games that can, and says so for games that
// cannot rather than pretending the archive is consistent.
func quiesceStep() Step {
	return Step{
		Name: "quiesce",
		Est:  2 * time.Second,
		Run: func(ctx context.Context, s *StepCtx) error {
			q, ok := s.Game.(interface {
				Quiesce(context.Context) (func(), error)
			})
			if !ok {
				s.Say("this game cannot pause writes; taking a hot copy")
				return nil
			}
			release, err := q.Quiesce(ctx)
			if err != nil {
				return err
			}
			s.Set("release", release)
			s.Say("writes paused")
			return nil
		},
		// Releasing is the compensation and also what the next step does on
		// the way out. It must always be safe to call, which is the
		// contract games.Backupable states.
		Undo: func(ctx context.Context, s *StepCtx) error {
			releaseWrites(s)
			return nil
		},
	}
}

func snapshotStep(archive Archiver) Step {
	return Step{
		Name: "snapshot volume",
		Est:  30 * time.Second,
		Run: func(ctx context.Context, s *StepCtx) error {
			defer releaseWrites(s)

			if s.Instance.Data == "" {
				return errors.New("no data directory configured, so there is nothing to archive")
			}

			path, size, err := archive.Create(ctx, s.Instance.Data, time.Now())
			if err != nil {
				return err
			}
			s.Set("archive", path)
			s.Say(fmt.Sprintf("archived %s", humanBytes(size)))
			return nil
		},
	}
}

// pruneStep keeps the newest few. It has no compensation on purpose: deleting
// old backups is not something to undo, and re-creating them is not possible.
func pruneStep(archive Archiver, keep int) Step {
	return Step{
		Name: "prune old backups",
		Est:  time.Second,
		Run: func(ctx context.Context, s *StepCtx) error {
			if keep <= 0 {
				return nil
			}
			removed, err := archive.Prune(keep)
			if err != nil {
				return err
			}
			if len(removed) > 0 {
				s.Say(fmt.Sprintf("removed %d older backup(s), keeping %d", len(removed), keep))
			}
			return nil
		},
	}
}

// releaseWrites resumes a paused game, at most once.
func releaseWrites(s *StepCtx) {
	release, ok := s.Values["release"].(func())
	if !ok || release == nil {
		return
	}
	delete(s.Values, "release")
	release()
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

// Update is the flagship composition, and everything else here is a subset of
// it: warn, save, stop, snapshot, pull, recreate, start, healthcheck.
//
// The snapshot is the compensation for every step after it. That is what makes
// cancelling at the pull and a failed healthcheck at the end have the same
// defined outcome — the world goes back to how it was and the previous image
// runs again. Without it, "the update failed" would mean something different
// depending on where it failed, which is the same as meaning nothing.
func Update(id, server string, trigger Trigger, archive Archiver, keep int) *Task {
	return &Task{
		ID: id, Server: server, Kind: KindUpdate, Trigger: trigger,
		Steps: []Step{
			quiesceStep(),
			snapshotStep(archive),
			stopStep(),
			pullStep(),
			removeStep(),
			createStep(),
			startStep(),
			healthStep(),
		},
	}
}

// pullStep fetches the image the plan asks for.
//
// Its compensation is nothing: an image already on disk is not something to
// take back, and the container is recreated from whichever image the plan
// names either way. The rollback that matters is the snapshot below it.
func pullStep() Step {
	return Step{
		Name: "pull image",
		Est:  2 * time.Minute,
		Run: func(ctx context.Context, s *StepCtx) error {
			plan, err := s.Game.Plan(s.Instance)
			if err != nil {
				return err
			}
			if plan.Image == "" {
				return errors.New("the game did not say which image to use")
			}

			last := ""
			return s.Driver.Pull(ctx, plan.Image, func(line string) {
				// The engine says the same thing about every layer, so only
				// changes are worth recording — otherwise the history is
				// four hundred lines of "Downloading".
				if line != last {
					last = line
					s.Say(line)
				}
			})
		},
	}
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
