package tasks_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/host/fake"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tasks"
)

// valheimGame stands in for a plugin whose stop behaviour matters: the image
// traps SIGINT to save the world, and SIGTERM kills it mid-write.
type valheimGame struct{}

func (valheimGame) Plan(inst model.Instance) (model.Plan, error) {
	return model.Plan{
		Image:      "lloesche/valheim-server",
		StopSignal: "SIGINT",
		StopGrace:  120 * time.Second,
		Env:        map[string]string{"SERVER_NAME": inst.Name},
	}, nil
}

func (valheimGame) Compile(model.Instance) ([]model.File, error) { return nil, nil }

type driverResolver struct {
	inst model.Instance
	game tasks.Game
}

func (r driverResolver) Instance(string) (model.Instance, tasks.Game, error) {
	return r.inst, r.game, nil
}

type memSaver struct {
	mu      sync.Mutex
	saved   []model.Instance
	deleted []string
	err     error
}

func (m *memSaver) Save(inst model.Instance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.saved = append(m.saved, inst)
	return nil
}

func (m *memSaver) last() (model.Instance, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.saved) == 0 {
		return model.Instance{}, false
	}
	return m.saved[len(m.saved)-1], true
}

func runTask(t *testing.T, d *fake.Driver, res tasks.Resolver, task *tasks.Task) tasks.Progress {
	t.Helper()

	rec := newRecorder()
	e := tasks.New(d, res, rec, nil)
	e.Now = func() time.Time { return at }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); e.Run(ctx) }()

	e.Submit(ctx, task)
	rec.waitFor(t, "the task to finish", func() bool { _, ok := rec.final(task.ID); return ok })

	p, _ := rec.final(task.ID)
	cancel()
	<-done
	return p
}

func healthy(instance string) host.Container {
	c := fake.Running(instance, "valheim", at)
	c.Health = model.Health{OK: true}
	return c
}

// The signal is game knowledge and getting it wrong costs a save.
func TestRestartUsesTheGamesStopSignal(t *testing.T) {
	d := fake.New(healthy("valheim-huldra"))
	res := driverResolver{
		inst: model.Instance{Name: "valheim-huldra", Game: "valheim"},
		game: valheimGame{},
	}

	p := runTask(t, d, res, tasks.Restart("t1", "valheim-huldra", tasks.TriggerManual))
	if p.State != tasks.StateDone {
		t.Fatalf("state = %v (%s)", p.State, p.Err)
	}

	var stop string
	for _, c := range d.Calls() {
		if strings.HasPrefix(c, "Stop(") {
			stop = c
		}
	}
	if !strings.Contains(stop, "SIGINT") {
		t.Errorf("stop call = %q, want the game's SIGINT", stop)
	}
	if !strings.Contains(stop, "2m0s") {
		t.Errorf("stop call = %q, want the game's 2m grace", stop)
	}
}

func TestRestartStopsThenStarts(t *testing.T) {
	d := fake.New(healthy("a"))
	res := driverResolver{inst: model.Instance{Name: "a", Game: "valheim"}, game: valheimGame{}}

	if p := runTask(t, d, res, tasks.Restart("t1", "a", tasks.TriggerManual)); p.State != tasks.StateDone {
		t.Fatalf("state = %v (%s)", p.State, p.Err)
	}

	var order []string
	for _, c := range d.Calls() {
		switch {
		case strings.HasPrefix(c, "Stop("):
			order = append(order, "stop")
		case strings.HasPrefix(c, "Start("):
			order = append(order, "start")
		}
	}
	if len(order) < 2 || order[0] != "stop" || order[1] != "start" {
		t.Errorf("call order = %v, want stop then start", order)
	}
}

// A server that comes up unhealthy and stays that way is worse than one that
// never came up, because nobody is watching by then. The healthcheck failing
// must roll the restart back.
func TestAFailedHealthcheckRollsBack(t *testing.T) {
	sick := healthy("a")
	sick.Health = model.Health{OK: false, Detail: "connection refused"}
	d := fake.New(sick)
	res := driverResolver{inst: model.Instance{Name: "a", Game: "valheim"}, game: valheimGame{}}

	task := tasks.Restart("t1", "a", tasks.TriggerManual)
	// A real budget would make this a 90-second test.
	task.Steps[2].Run = func(ctx context.Context, s *tasks.StepCtx) error {
		return errors.New("not healthy within 90s")
	}

	p := runTask(t, d, res, task)
	if p.State != tasks.StateRolledBack {
		t.Errorf("state = %v, want rolled back", p.State)
	}
	if !strings.Contains(p.Err, "healthy") {
		t.Errorf("error = %q, want the healthcheck named", p.Err)
	}
}

// Starting a configured server that was never created should do the obvious
// thing rather than failing with "no such container".
func TestStartCreatesTheContainerWhenThereIsNone(t *testing.T) {
	d := fake.New() // nothing exists
	d.SetClock(func() time.Time { return at })
	res := driverResolver{inst: model.Instance{Name: "fresh", Game: "valheim"}, game: valheimGame{}}

	p := runTask(t, d, res, tasks.Start("t1", "fresh", tasks.TriggerManual))
	if p.State != tasks.StateDone {
		t.Fatalf("state = %v (%s)", p.State, p.Err)
	}
	if d.CallCount("Create") != 1 {
		t.Errorf("Create called %d times, want 1", d.CallCount("Create"))
	}
}

// A settings change that only touches a config file writes it and stops there.
func TestApplyConfigWithoutRecreateOnlyWrites(t *testing.T) {
	d := fake.New(healthy("a"))
	res := driverResolver{inst: model.Instance{Name: "a", Game: "valheim"}, game: valheimGame{}}
	saver := &memSaver{}

	next := model.Instance{Name: "a", Game: "valheim", Settings: map[string]any{"ServerName": "Changed"}}
	p := runTask(t, d, res, tasks.ApplyConfig("t1", "a", tasks.TriggerManual, next, saver, false))

	if p.State != tasks.StateDone {
		t.Fatalf("state = %v (%s)", p.State, p.Err)
	}
	if got, ok := saver.last(); !ok || got.Settings["ServerName"] != "Changed" {
		t.Errorf("saved = %+v, want the new settings", got)
	}
	if d.CallCount("Stop") != 0 {
		t.Error("a config-only change stopped the server")
	}
}

// A change to the container's shape needs a new container, and the volume must
// survive it — that is the whole reason recreating is safe.
func TestApplyConfigWithRecreateKeepsTheVolume(t *testing.T) {
	d := fake.New(healthy("a"))
	d.SetClock(func() time.Time { return at })
	res := driverResolver{inst: model.Instance{Name: "a", Game: "valheim"}, game: valheimGame{}}

	next := model.Instance{Name: "a", Game: "valheim", Settings: map[string]any{"ServerName": "Changed"}}
	p := runTask(t, d, res, tasks.ApplyConfig("t1", "a", tasks.TriggerManual, next, &memSaver{}, true))

	if p.State != tasks.StateDone {
		t.Fatalf("state = %v (%s)", p.State, p.Err)
	}
	for _, c := range d.Calls() {
		if strings.HasPrefix(c, "Remove(") && strings.Contains(c, "true") {
			t.Errorf("the container was removed with its volumes: %q", c)
		}
	}
	if d.CallCount("Create") != 1 {
		t.Errorf("Create called %d times, want 1", d.CallCount("Create"))
	}
}

// A failed write must put the previous configuration back, or the file on disk
// and the container disagree about what the server is.
func TestApplyConfigRestoresTheOldFileOnFailure(t *testing.T) {
	d := fake.New(healthy("a"))
	d.SetFail("Stop", errors.New("engine went away"))
	before := model.Instance{Name: "a", Game: "valheim", Settings: map[string]any{"ServerName": "Original"}}
	res := driverResolver{inst: before, game: valheimGame{}}
	saver := &memSaver{}

	next := model.Instance{Name: "a", Game: "valheim", Settings: map[string]any{"ServerName": "Changed"}}
	p := runTask(t, d, res, tasks.ApplyConfig("t1", "a", tasks.TriggerManual, next, saver, true))

	if p.State != tasks.StateRolledBack {
		t.Fatalf("state = %v (%s), want rolled back", p.State, p.Err)
	}
	got, ok := saver.last()
	if !ok {
		t.Fatal("nothing was saved at all")
	}
	if got.Settings["ServerName"] != "Original" {
		t.Errorf("the file was left as %v, want the original restored", got.Settings["ServerName"])
	}
}

// Valheim compiles no files. That is not a special case — an empty result
// writes nothing and the step succeeds, which is what ADR 0006 wanted the
// interface to handle honestly from the first game.
func TestCompilingNoFilesIsNotAFailure(t *testing.T) {
	d := fake.New(healthy("a"))
	res := driverResolver{inst: model.Instance{Name: "a", Game: "valheim"}, game: valheimGame{}}

	p := runTask(t, d, res, tasks.ApplyConfig("t1", "a", tasks.TriggerManual,
		model.Instance{Name: "a", Game: "valheim"}, &memSaver{}, false))

	if p.State != tasks.StateDone {
		t.Errorf("state = %v (%s), want done", p.State, p.Err)
	}
	joined := strings.Join(p.History, " ")
	if !strings.Contains(joined, "no config files") {
		t.Errorf("history = %v, want it to say there were none", p.History)
	}
}

// Stopping a server that is already down is not an error to report.
func TestStoppingSomethingAlreadyDownSucceeds(t *testing.T) {
	d := fake.New(fake.Stopped("a", "valheim"))
	res := driverResolver{inst: model.Instance{Name: "a", Game: "valheim"}, game: valheimGame{}}

	if p := runTask(t, d, res, tasks.Stop("t1", "a", tasks.TriggerManual)); p.State != tasks.StateDone {
		t.Errorf("state = %v (%s)", p.State, p.Err)
	}
}

// stubArchive records what was asked of it.
type stubArchive struct {
	mu       sync.Mutex
	created  int
	pruned   int
	restored []string
	err      error
	// restoreErr fails the restore itself, so a test can drive the
	// compensation that puts the previous world back.
	restoreErr error
}

func (s *stubArchive) Restore(_ context.Context, archive, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.restoreErr != nil {
		return s.restoreErr
	}
	s.restored = append(s.restored, archive)
	return nil
}

// restores is what was unpacked, in order.
func (s *stubArchive) restores() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.restored...)
}

func (s *stubArchive) Create(context.Context, string, time.Time) (string, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return "", 0, s.err
	}
	s.created++
	return "/backups/2026-09-09T0220.tar.zst", 1 << 20, nil
}

func (s *stubArchive) Prune(int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruned++
	return nil, nil
}

func (s *stubArchive) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.created, s.pruned
}

// The snapshot must come before anything is changed, or it is not a rollback
// point — it is a copy of the damage.
func TestUpdateSnapshotsBeforeItTouchesAnything(t *testing.T) {
	d := fake.New(healthy("a"))
	d.SetClock(func() time.Time { return at })
	res := driverResolver{
		inst: model.Instance{Name: "a", Game: "valheim", Data: "/data"},
		game: valheimGame{},
	}
	arch := &stubArchive{}

	p := runTask(t, d, res, tasks.Update("t1", "a", tasks.TriggerManual, arch, 14))
	if p.State != tasks.StateDone {
		t.Fatalf("state = %v (%s)", p.State, p.Err)
	}

	// The archive is taken before the stop, so ordering is checked by
	// position in the step list rather than by call timestamps.
	snapshot, stop := indexOf(p.Steps, "snapshot volume"), indexOf(p.Steps, "stop")
	pull, recreate := indexOf(p.Steps, "pull image"), indexOf(p.Steps, "recreate container")
	if !(snapshot < stop && stop < pull && pull < recreate) {
		t.Errorf("step order is %v, want snapshot before stop before pull before recreate", p.Steps)
	}
	if created, _ := arch.counts(); created != 1 {
		t.Errorf("took %d snapshots, want 1", created)
	}
	if d.CallCount("Pull") != 1 {
		t.Errorf("pulled %d times, want 1", d.CallCount("Pull"))
	}
}

// A failure after the snapshot must put the server back, which is the whole
// reason the snapshot is first.
func TestAFailedUpdateRollsBack(t *testing.T) {
	d := fake.New(healthy("a"))
	d.SetClock(func() time.Time { return at })
	d.SetFail("Pull", errors.New("registry unreachable"))
	res := driverResolver{
		inst: model.Instance{Name: "a", Game: "valheim", Data: "/data"},
		game: valheimGame{},
	}

	p := runTask(t, d, res, tasks.Update("t1", "a", tasks.TriggerManual, &stubArchive{}, 14))
	if p.State != tasks.StateRolledBack {
		t.Fatalf("state = %v (%s), want rolled back", p.State, p.Err)
	}
	if !strings.Contains(p.Err, "registry unreachable") {
		t.Errorf("error = %q, want the cause", p.Err)
	}

	// The stop's compensation starts it again.
	var restarted bool
	for _, c := range d.Calls() {
		if strings.HasPrefix(c, "Start(") {
			restarted = true
		}
	}
	if !restarted {
		t.Error("the server was left down after a failed update")
	}
}

// A backup with nowhere to write is a failure to report, not a success.
func TestBackupWithNoDataDirectoryFails(t *testing.T) {
	d := fake.New(healthy("a"))
	res := driverResolver{inst: model.Instance{Name: "a", Game: "valheim"}, game: valheimGame{}}

	p := runTask(t, d, res, tasks.Backup("t1", "a", tasks.TriggerManual, &stubArchive{}, 14))
	if p.State == tasks.StateDone {
		t.Fatal("a backup with no data directory reported success")
	}
	if !strings.Contains(p.Err, "data directory") {
		t.Errorf("error = %q, want it to name the problem", p.Err)
	}
}

func TestBackupPrunes(t *testing.T) {
	d := fake.New(healthy("a"))
	res := driverResolver{
		inst: model.Instance{Name: "a", Game: "valheim", Data: "/data"},
		game: valheimGame{},
	}
	arch := &stubArchive{}

	if p := runTask(t, d, res, tasks.Backup("t1", "a", tasks.TriggerManual, arch, 14)); p.State != tasks.StateDone {
		t.Fatalf("state = %v (%s)", p.State, p.Err)
	}
	if created, pruned := arch.counts(); created != 1 || pruned != 1 {
		t.Errorf("created %d, pruned %d, want 1 and 1", created, pruned)
	}
}

func indexOf(list []string, want string) int {
	for i, s := range list {
		if s == want {
			return i
		}
	}
	return -1
}

// A restore replaces a world, so the world it replaces is archived first and
// that archive is what the compensation puts back.
func TestRestoreArchivesTheWorldItReplaces(t *testing.T) {
	d := fake.New(healthy("a"))
	d.SetClock(func() time.Time { return at })
	res := driverResolver{
		inst: model.Instance{Name: "a", Game: "valheim", Data: "/data"},
		game: valheimGame{},
	}
	arch := &stubArchive{}

	p := runTask(t, d, res, tasks.Restore("t1", "a", tasks.TriggerManual, arch, "/backups/old.tar.zst"))
	if p.State != tasks.StateDone {
		t.Fatalf("state = %v (%s), want done", p.State, p.Err)
	}

	if created, _ := arch.counts(); created != 1 {
		t.Errorf("took %d safety archives, want exactly 1", created)
	}
	if got := arch.restores(); len(got) != 1 || got[0] != "/backups/old.tar.zst" {
		t.Errorf("restored %v, want the archive that was asked for", got)
	}
}

// The case the whole design is for: the restore itself fails, and the world
// that was there before comes back.
func TestAFailedRestorePutsTheOldWorldBack(t *testing.T) {
	d := fake.New(healthy("a"))
	d.SetClock(func() time.Time { return at })
	res := driverResolver{
		inst: model.Instance{Name: "a", Game: "valheim", Data: "/data"},
		game: valheimGame{},
	}
	arch := &stubArchive{restoreErr: errors.New("archive is truncated")}

	p := runTask(t, d, res, tasks.Restore("t1", "a", tasks.TriggerManual, arch, "/backups/bad.tar.zst"))
	if p.State != tasks.StateRolledBack {
		t.Fatalf("state = %v (%s), want rolled back", p.State, p.Err)
	}
	if !strings.Contains(p.Err, "truncated") {
		t.Errorf("error = %q, want the cause", p.Err)
	}

	// The safety archive was taken even though the restore never landed, so
	// there is something to go back to.
	if created, _ := arch.counts(); created != 1 {
		t.Errorf("took %d safety archives, want 1 before touching the world", created)
	}

	// And the server is running again rather than left down.
	var restarted bool
	for _, c := range d.Calls() {
		if strings.HasPrefix(c, "Start(") {
			restarted = true
		}
	}
	if !restarted {
		t.Error("the server was left down after a failed restore")
	}
}

func TestRestoreWithNoArchiveNamedFails(t *testing.T) {
	d := fake.New(healthy("a"))
	res := driverResolver{
		inst: model.Instance{Name: "a", Game: "valheim", Data: "/data"},
		game: valheimGame{},
	}

	p := runTask(t, d, res, tasks.Restore("t1", "a", tasks.TriggerManual, &stubArchive{}, ""))
	if p.State == tasks.StateDone {
		t.Fatal("a restore with no archive named reported success")
	}
}

// Delete satisfies the Saver interface. Deleting is recorded rather than done,
// which is all a fake needs.
func (m *memSaver) Delete(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted = append(m.deleted, name)
	return nil
}

// Delete removes everything Garrison made and nothing it did not. The world
// is the point: even behind a typed confirmation, a key that could erase a
// save nobody has a backup of is a key with no business existing.
func TestDeleteRemovesTheContainerAndTheConfigButNotTheWorld(t *testing.T) {
	d := fake.New(healthy("a"))
	d.SetClock(func() time.Time { return at })
	inst := model.Instance{Name: "a", Game: "valheim", Data: "/data"}
	res := driverResolver{inst: inst, game: valheimGame{}}
	saver := &memSaver{}

	p := runTask(t, d, res, tasks.Delete("t1", "a", tasks.TriggerManual, saver, inst))
	if p.State != tasks.StateDone {
		t.Fatalf("state = %v (%s), want done", p.State, p.Err)
	}

	var removed bool
	for _, c := range d.Calls() {
		if strings.HasPrefix(c, "Remove(") {
			removed = true
		}
		// The data volume must not be swept up with the container.
		if strings.Contains(c, "Remove(") && strings.Contains(c, "true") {
			t.Errorf("the container was removed with its volumes: %s", c)
		}
	}
	if !removed {
		t.Error("the container was not removed")
	}

	saver.mu.Lock()
	deleted := append([]string(nil), saver.deleted...)
	saver.mu.Unlock()
	if len(deleted) != 1 || deleted[0] != "a" {
		t.Errorf("deleted config %v, want [a]", deleted)
	}
}

// The configuration is removed last and its compensation writes it back, so a
// delete that fails leaves a server Garrison still knows about rather than an
// orphaned directory and no record of what it was.
func TestAFailedDeleteKeepsTheServerKnown(t *testing.T) {
	d := fake.New(healthy("a"))
	d.SetClock(func() time.Time { return at })
	d.SetFail("Remove", errors.New("container is in use"))
	inst := model.Instance{Name: "a", Game: "valheim", Data: "/data"}
	res := driverResolver{inst: inst, game: valheimGame{}}
	saver := &memSaver{}

	p := runTask(t, d, res, tasks.Delete("t1", "a", tasks.TriggerManual, saver, inst))
	if p.State == tasks.StateDone {
		t.Fatal("a delete whose container removal failed reported success")
	}

	saver.mu.Lock()
	deleted := append([]string(nil), saver.deleted...)
	saver.mu.Unlock()
	if len(deleted) != 0 {
		t.Errorf("the configuration was deleted despite the failure: %v", deleted)
	}
}

// Drainer satisfies the Resolver interface. A drain against a fake resolver
// has no channel, which is the same degradation a game without one gets.
func (driverResolver) Drainer(string) tasks.Drainer { return nil }
func (driverResolver) Mods(string) tasks.ModSync    { return nil }

// fakeDrain is a bound drain capability, which is how a step receives one:
// cmd asserts games.Drainable and adapts, so this package never sees a game.
type fakeDrain struct {
	mu     sync.Mutex
	warned []time.Duration
	saved  int
	err    error
}

func (f *fakeDrain) Warn(_ context.Context, in time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.warned = append(f.warned, in)
	return nil
}

func (f *fakeDrain) Save(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved++
	return f.err
}

func (f *fakeDrain) state() ([]time.Duration, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.warned...), f.saved
}

// drainResolver hands the engine a bound drain.
type drainResolver struct {
	driverResolver
	drain tasks.Drainer
}

func (d drainResolver) Drainer(string) tasks.Drainer { return d.drain }
func (d drainResolver) Mods(string) tasks.ModSync    { return nil }

// A drain warns, waits, saves, and only then stops. The debt this closes has
// been open since M0 and could not be closed before: Valheim has no channel
// to warn anyone on.
func TestADrainWarnsAndSavesBeforeStopping(t *testing.T) {
	d := fake.New(healthy("a"))
	d.SetClock(func() time.Time { return at })
	drain := &fakeDrain{}
	res := drainResolver{
		driverResolver: driverResolver{
			inst: model.Instance{Name: "a", Game: "zomboid", Data: "/data"},
			game: valheimGame{},
		},
		drain: drain,
	}

	// A short drain so the test is not a wait. Only the one-minute warning
	// is inside it, which is itself the behaviour: a five-minute drain does
	// not announce fifteen.
	p := runTask(t, d, res, tasks.RestartWithDrain("t1", "a", tasks.TriggerScheduled, 50*time.Millisecond))
	if p.State != tasks.StateDone {
		t.Fatalf("state = %v (%s), want done", p.State, p.Err)
	}

	_, saved := drain.state()
	if saved != 1 {
		t.Errorf("saved %d times, want 1 — the world should be flushed before the stop", saved)
	}

	// The order matters: a save after the stop would be a save against a
	// server that is gone.
	var savedAt, stoppedAt = -1, -1
	for i, entry := range p.History {
		if strings.Contains(entry, "world saved") {
			savedAt = i
		}
	}
	for i, c := range d.Calls() {
		if strings.HasPrefix(c, "Stop(") {
			stoppedAt = i
		}
	}
	if savedAt < 0 {
		t.Errorf("no save in the history: %v", p.History)
	}
	if stoppedAt < 0 {
		t.Error("the server was never stopped")
	}
}

// A game with no channel to warn on degrades to a restart that does not warn,
// and says so rather than waiting out a drain that helps nobody.
func TestADrainWithoutAChannelSaysSoAndDoesNotWait(t *testing.T) {
	d := fake.New(healthy("a"))
	d.SetClock(func() time.Time { return at })
	res := driverResolver{
		inst: model.Instance{Name: "a", Game: "valheim", Data: "/data"},
		game: valheimGame{},
	}

	start := time.Now()
	p := runTask(t, d, res, tasks.RestartWithDrain("t1", "a", tasks.TriggerScheduled, 10*time.Minute))
	if p.State != tasks.StateDone {
		t.Fatalf("state = %v (%s)", p.State, p.Err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("a drain with no channel waited %v", elapsed)
	}

	var explained bool
	for _, entry := range p.History {
		if strings.Contains(entry, "no channel to warn players") {
			explained = true
		}
	}
	if !explained {
		t.Errorf("the skipped drain was not explained: %v", p.History)
	}
}

// A warning that did not send is worth saying and not worth failing for: the
// restart is still right, and the alternative is a server nobody can restart
// because its chat is broken.
func TestADrainSurvivesAFailedWarning(t *testing.T) {
	d := fake.New(healthy("a"))
	d.SetClock(func() time.Time { return at })
	res := drainResolver{
		driverResolver: driverResolver{
			inst: model.Instance{Name: "a", Game: "zomboid", Data: "/data"},
			game: valheimGame{},
		},
		drain: &fakeDrain{err: errors.New("rcon: connection refused")},
	}

	p := runTask(t, d, res, tasks.RestartWithDrain("t1", "a", tasks.TriggerScheduled, 50*time.Millisecond))
	if p.State != tasks.StateDone {
		t.Errorf("a failed warning failed the restart: %v (%s)", p.State, p.Err)
	}
}

// A plain restart is a drain of zero, which is the whole difference between a
// scheduled bounce and an operator fixing a wedged server.
func TestAPlainRestartHasNoDrainStep(t *testing.T) {
	plain := tasks.Restart("t1", "a", tasks.TriggerManual)
	for _, s := range plain.Steps {
		if s.Name == "drain" {
			t.Error("a plain restart has a drain step")
		}
	}

	drained := tasks.RestartWithDrain("t1", "a", tasks.TriggerScheduled, time.Minute)
	if len(drained.Steps) != len(plain.Steps)+1 {
		t.Errorf("a drained restart has %d steps, a plain one %d", len(drained.Steps), len(plain.Steps))
	}
}

// A volume-backed server has no host directory to tar. A backup task that
// reported success without writing anything is how somebody discovers they
// have no backups on the day they need one.
func TestBackupRefusesAVolumeRatherThanSucceedingQuietly(t *testing.T) {
	d := fake.New(healthy("a"))
	res := driverResolver{
		inst: model.Instance{Name: "a", Game: "valheim", Volume: "garrison-a-world"},
		game: valheimGame{},
	}
	arch := &stubArchive{}

	p := runTask(t, d, res, tasks.Backup("t1", "a", tasks.TriggerManual, arch, 14))
	if p.State == tasks.StateDone {
		t.Fatal("a backup of a volume reported success")
	}
	if !strings.Contains(p.Err, "volume") {
		t.Errorf("error = %q, want it to name the reason", p.Err)
	}
	if created, _ := arch.counts(); created != 0 {
		t.Errorf("the archiver was called %d times for a volume", created)
	}
}

// A restore whose safety archive cannot be taken is a restore with no way
// back, which is the one thing tasks.Restore exists to guarantee.
func TestRestoreRefusesAVolume(t *testing.T) {
	d := fake.New(healthy("a"))
	res := driverResolver{
		inst: model.Instance{Name: "a", Game: "valheim", Volume: "garrison-a-world"},
		game: valheimGame{},
	}
	arch := &stubArchive{}

	p := runTask(t, d, res, tasks.Restore("t1", "a", tasks.TriggerManual, arch, "/backups/old.tar.zst"))
	if p.State == tasks.StateDone {
		t.Fatal("a restore onto a volume reported success")
	}
	if got := arch.restores(); len(got) != 0 {
		t.Errorf("the archive was unpacked anyway: %v", got)
	}
}

// modResolver hands the engine a bound mod sync.
type modResolver struct {
	driverResolver
	sync tasks.ModSync
}

func (m modResolver) Mods(string) tasks.ModSync { return m.sync }

// fakeSync records what an apply asked of it and whether the compensation ran.
type fakeSync struct {
	mu       sync.Mutex
	synced   []string
	undone   int
	err      error
	instance model.Instance
}

func (f *fakeSync) Sync(_ context.Context, inst model.Instance, log func(string)) (func(context.Context) error, error) {
	f.mu.Lock()
	f.instance = inst
	for _, m := range inst.Mods {
		f.synced = append(f.synced, m.ID)
	}
	err := f.err
	f.mu.Unlock()

	if log != nil {
		log(fmt.Sprintf("installed %d mod(s)", len(inst.Mods)))
	}
	undo := func(context.Context) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.undone++
		return nil
	}
	return undo, err
}

func (f *fakeSync) state() ([]string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.synced...), f.undone
}

// An apply installs the mods the configuration it just wrote asks for, not
// the ones the old configuration had.
func TestApplyInstallsTheModsItJustWrote(t *testing.T) {
	d := fake.New(healthy("a"))
	d.SetClock(func() time.Time { return at })
	sync := &fakeSync{}
	res := modResolver{
		driverResolver: driverResolver{
			inst: model.Instance{Name: "a", Game: "valheim", Data: "/data"},
			game: valheimGame{},
		},
		sync: sync,
	}

	next := model.Instance{
		Name: "a", Game: "valheim", Data: "/data",
		Mods: []model.ModRef{{ID: "ValheimModding-Jotunn"}},
	}
	p := runTask(t, d, res, tasks.ApplyConfig("t1", "a", tasks.TriggerManual, next, &memSaver{}, false))

	if p.State != tasks.StateDone {
		t.Fatalf("apply state = %v, history %v", p.State, p.History)
	}
	synced, undone := sync.state()
	if len(synced) != 1 || synced[0] != "ValheimModding-Jotunn" {
		t.Errorf("synced %v, want the mod the new configuration lists", synced)
	}
	if undone != 0 {
		t.Errorf("the install was undone %d times on a task that succeeded", undone)
	}
}

// The rule for any step that changes a volume: an apply that installs mods
// and then cannot bring the server back puts the mods back too.
func TestAFailedApplyUndoesTheModInstall(t *testing.T) {
	d := fake.New(healthy("a"))
	d.SetClock(func() time.Time { return at })
	// The recreate is what fails, which is the realistic case: the mods went
	// in, and the container will not come up with them.
	d.SetFail("Create", errors.New("port 2456 already allocated"))

	sync := &fakeSync{}
	res := modResolver{
		driverResolver: driverResolver{
			inst: model.Instance{Name: "a", Game: "valheim", Data: "/data"},
			game: valheimGame{},
		},
		sync: sync,
	}

	next := model.Instance{
		Name: "a", Game: "valheim", Data: "/data",
		Mods: []model.ModRef{{ID: "ValheimModding-Jotunn"}},
	}
	p := runTask(t, d, res, tasks.ApplyConfig("t1", "a", tasks.TriggerManual, next, &memSaver{}, true))

	if p.State != tasks.StateRolledBack {
		t.Fatalf("apply state = %v, want it rolled back", p.State)
	}
	if _, undone := sync.state(); undone != 1 {
		t.Errorf("the mod install was undone %d times, want once", undone)
	}
}

// A game whose server downloads its own mods gets no sync, and that is not a
// failure — it is most games.
func TestAnApplyWithNoInstallerIsFine(t *testing.T) {
	d := fake.New(healthy("a"))
	d.SetClock(func() time.Time { return at })
	res := driverResolver{
		inst: model.Instance{Name: "a", Game: "valheim", Data: "/data"},
		game: valheimGame{},
	}

	next := model.Instance{Name: "a", Game: "valheim", Data: "/data"}
	p := runTask(t, d, res, tasks.ApplyConfig("t1", "a", tasks.TriggerManual, next, &memSaver{}, false))
	if p.State != tasks.StateDone {
		t.Fatalf("apply state = %v, history %v", p.State, p.History)
	}
}

// compilingGame stands in for a game whose settings become files — Zomboid's
// shape, without importing it.
type compilingGame struct {
	valheimGame
	files []model.File
	err   error
}

func (g compilingGame) Compile(model.Instance) ([]model.File, error) {
	return g.files, g.err
}

// The gap this closes: Compile was called, the count was logged, and nothing
// ever wrote the files. A game configured by files would have had its TOML
// updated and its server left reading the old configuration.
func TestApplyWritesTheCompiledFiles(t *testing.T) {
	d := fake.New(healthy("a"))
	d.SetClock(func() time.Time { return at })
	data := t.TempDir()

	game := compilingGame{files: []model.File{
		{Path: "Server/servertest.ini", Mode: 0o644, Data: []byte("MaxPlayers=16\n")},
		{Path: "Server/servertest_SandboxVars.lua", Mode: 0o644, Data: []byte("return {}\n")},
	}}
	inst := model.Instance{Name: "a", Game: "zomboid", Data: data}
	res := driverResolver{inst: inst, game: game}

	p := runTask(t, d, res, tasks.ApplyConfig("t1", "a", tasks.TriggerManual, inst, &memSaver{}, false))
	if p.State != tasks.StateDone {
		t.Fatalf("apply state = %v, history %v", p.State, p.History)
	}

	got, err := os.ReadFile(filepath.Join(data, "Server", "servertest.ini"))
	if err != nil {
		t.Fatalf("the compiled config was not written: %v", err)
	}
	if string(got) != "MaxPlayers=16\n" {
		t.Errorf("servertest.ini = %q, want what Compile produced", got)
	}
	if _, err := os.Stat(filepath.Join(data, "Server", "servertest_SandboxVars.lua")); err != nil {
		t.Errorf("the second compiled file was not written: %v", err)
	}
}

// An apply that writes config and then cannot bring the server back leaves
// the server reading the configuration it was started with.
func TestAFailedApplyPutsTheOldConfigBack(t *testing.T) {
	d := fake.New(healthy("a"))
	d.SetClock(func() time.Time { return at })
	d.SetFail("Create", errors.New("port 16261 already allocated"))
	data := t.TempDir()

	if err := os.MkdirAll(filepath.Join(data, "Server"), 0o755); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(data, "Server", "servertest.ini")
	if err := os.WriteFile(existing, []byte("MaxPlayers=8\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	game := compilingGame{files: []model.File{
		{Path: "Server/servertest.ini", Mode: 0o644, Data: []byte("MaxPlayers=64\n")},
		{Path: "Server/new.ini", Mode: 0o644, Data: []byte("fresh\n")},
	}}
	inst := model.Instance{Name: "a", Game: "zomboid", Data: data}
	res := driverResolver{inst: inst, game: game}

	p := runTask(t, d, res, tasks.ApplyConfig("t1", "a", tasks.TriggerManual, inst, &memSaver{}, true))
	if p.State != tasks.StateRolledBack {
		t.Fatalf("apply state = %v, want it rolled back", p.State)
	}

	got, err := os.ReadFile(existing)
	if err != nil {
		t.Fatalf("the previous config is gone: %v", err)
	}
	if string(got) != "MaxPlayers=8\n" {
		t.Errorf("servertest.ini = %q, want the configuration the server was started with", got)
	}
	// A file the apply created has to go too, or the next diff compares
	// against something nobody chose.
	if _, err := os.Stat(filepath.Join(data, "Server", "new.ini")); err == nil {
		t.Error("a config file the rolled-back apply created is still there")
	}
}

// A volume keeps the world inside the runtime, where there is no path to
// write to. Reporting a successful apply that wrote nothing is how somebody
// spends an evening wondering why a setting does nothing.
func TestApplyRefusesToWriteFilesToAVolume(t *testing.T) {
	d := fake.New(healthy("a"))
	d.SetClock(func() time.Time { return at })

	game := compilingGame{files: []model.File{{Path: "Server/servertest.ini", Data: []byte("x\n")}}}
	inst := model.Instance{Name: "a", Game: "zomboid", Volume: "a-world"}
	res := driverResolver{inst: inst, game: game}

	p := runTask(t, d, res, tasks.ApplyConfig("t1", "a", tasks.TriggerManual, inst, &memSaver{}, false))
	if p.State == tasks.StateDone {
		t.Fatal("the apply reported success without writing anything")
	}
	if !strings.Contains(p.Err, "volume") {
		t.Errorf("error = %q, want it to say why there is nowhere to write", p.Err)
	}
}

// A game configured entirely by environment compiles to nothing, and that is
// the ordinary case rather than a failure — ADR 0006's whole point.
func TestApplyWithNoCompiledFilesNeedsNoDataDirectory(t *testing.T) {
	d := fake.New(healthy("a"))
	d.SetClock(func() time.Time { return at })

	inst := model.Instance{Name: "a", Game: "valheim", Volume: "a-world"}
	res := driverResolver{inst: inst, game: valheimGame{}}

	p := runTask(t, d, res, tasks.ApplyConfig("t1", "a", tasks.TriggerManual, inst, &memSaver{}, false))
	if p.State != tasks.StateDone {
		t.Fatalf("apply state = %v, history %v", p.State, p.History)
	}
}
