package tasks_test

import (
	"context"
	"errors"
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
	mu    sync.Mutex
	saved []model.Instance
	err   error
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
