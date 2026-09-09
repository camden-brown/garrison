// Package fake is an in-memory host.Driver for tests.
//
// It exists so `go test ./...` passes with Docker stopped, which is the
// property that keeps the suite worth running. Everything above internal/host
// — the store, the fleet poller, the views — is tested against this and never
// against an engine.
//
// It is a real implementation, not a stub: Start and Stop move containers
// between states, so a test can drive the store end to end and assert on what
// the fleet view would draw.
package fake

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/model"
)

// Driver records every call and answers from memory.
//
// The mutex here is not the concurrency leak the dependency rules warn about.
// That rule is about program state, which lives behind the single writer in
// internal/core. This is a test double whose whole job is to be called from
// the goroutines under test — a poller on its ticker while the test asserts —
// and the race detector is the reason those assertions mean anything.
type Driver struct {
	mu         sync.Mutex
	containers map[string]host.Container
	order      []string // insertion order, so List is deterministic
	calls      []string

	// Everything below is behind the mutex and reached through setters
	// rather than exported fields. A test that flips a field directly while
	// a poller is mid-call races with it, and the race detector is the whole
	// reason those tests are worth writing.
	down    bool
	fail    map[string]error
	samples []host.Sample
	logText string
	execOut map[string][]byte
	clock   func() time.Time
	info    host.Info
}

var _ host.Driver = (*Driver)(nil)

// ErrEngineDown is what an unreachable engine returns. It reads like the real
// thing on screen, which matters because the fleet view renders the message.
var ErrEngineDown = errors.New("cannot connect to the Docker daemon: is the docker daemon running")

// New returns a driver holding the given containers, in the order given.
func New(cs ...host.Container) *Driver {
	d := &Driver{
		containers: make(map[string]host.Container, len(cs)),
		execOut:    map[string][]byte{},
		fail:       map[string]error{},
	}
	for _, c := range cs {
		d.Put(c)
	}
	return d
}

// SetDown makes every call fail with ErrEngineDown, or stops it doing so.
// This is how the "Docker is not running" path gets tested: the fleet goes
// unknown rather than empty. Safe to call while a poller is running, which is
// the point — the interesting test is the engine vanishing mid-flight.
func (d *Driver) SetDown(down bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.down = down
}

// SetFail overrides the result of one method by name ("Start", "List"). Pass a
// nil error to clear it.
func (d *Driver) SetFail(method string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fail == nil {
		d.fail = map[string]error{}
	}
	if err == nil {
		delete(d.fail, method)
		return
	}
	d.fail[method] = err
}

// SetSamples is what Stats replays, one per receive.
func (d *Driver) SetSamples(samples ...host.Sample) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.samples = append([]host.Sample(nil), samples...)
}

// SetLogs is what Logs returns.
func (d *Driver) SetLogs(text string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.logText = text
}

// SetExec answers one Exec, keyed by argv.
func (d *Driver) SetExec(argv []string, out []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.execOut == nil {
		d.execOut = map[string][]byte{}
	}
	d.execOut[strings.Join(argv, " ")] = out
}

// SetClock fixes the time a started container records, so a test can assert on
// an uptime instead of measuring one.
func (d *Driver) SetClock(now func() time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.clock = now
}

// Running is a convenience constructor for the common fixture: a healthy
// container of a game, up since a known time.
func Running(instance, game string, since time.Time) host.Container {
	return host.Container{
		ID:       "c0ffee" + instance,
		Name:     host.ContainerName(instance),
		Instance: instance,
		Game:     game,
		PlanHash: "0000000000000000",
		State:    model.StateRunning,
		Started:  since,
		Health:   model.Health{OK: true},
	}
}

// Stopped is the other common fixture: deliberately down, exit 0.
func Stopped(instance, game string) host.Container {
	return host.Container{
		ID:       "c0ffee" + instance,
		Name:     host.ContainerName(instance),
		Instance: instance,
		Game:     game,
		PlanHash: "0000000000000000",
		State:    model.StateStopped,
	}
}

// Put adds or replaces a container.
func (d *Driver) Put(c host.Container) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.containers == nil {
		// A zero Driver is usable. Writing to a nil map panics, and a test
		// double that panics on its own zero value is a trap.
		d.containers = map[string]host.Container{}
	}
	if _, exists := d.containers[c.ID]; !exists {
		d.order = append(d.order, c.ID)
	}
	d.containers[c.ID] = c
}

// Calls returns the method calls made so far, in order, as "Method(arg)".
func (d *Driver) Calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}

// CallCount counts calls to one method.
func (d *Driver) CallCount(method string) int {
	n := 0
	for _, c := range d.Calls() {
		if strings.HasPrefix(c, method+"(") {
			n++
		}
	}
	return n
}

func (d *Driver) record(method string, args ...string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, method+"("+strings.Join(args, ",")+")")
	if d.down {
		return ErrEngineDown
	}
	return d.fail[method]
}

func (d *Driver) now() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.clock != nil {
		return d.clock()
	}
	return time.Time{}
}

func (d *Driver) Ping(ctx context.Context) error {
	return d.record("Ping")
}

// SetInfo fixes what Info reports.
func (d *Driver) SetInfo(info host.Info) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.info = info
}

func (d *Driver) Pull(ctx context.Context, ref string, progress func(string)) error {
	if err := d.record("Pull", ref); err != nil {
		return err
	}
	if progress != nil {
		progress("Pulling from " + ref)
		progress("Status: Downloaded newer image for " + ref)
	}
	return nil
}

func (d *Driver) Info(ctx context.Context) (host.Info, error) {
	if err := d.record("Info"); err != nil {
		return host.Info{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.info.NCPU == 0 {
		// A plausible default, so a test that does not care still gets
		// figures a view can divide by.
		return host.Info{Version: "29.0.0", OS: "fake", NCPU: 8, MemTotal: 16 << 30}, nil
	}
	return d.info, nil
}

func (d *Driver) List(ctx context.Context) ([]host.Container, error) {
	if err := d.record("List"); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]host.Container, 0, len(d.order))
	for _, id := range d.order {
		out = append(out, d.containers[id])
	}
	return out, nil
}

func (d *Driver) Inspect(ctx context.Context, id string) (host.Container, error) {
	if err := d.record("Inspect", id); err != nil {
		return host.Container{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.containers[id]
	if !ok {
		return host.Container{}, fmt.Errorf("no such container: %s", id)
	}
	return c, nil
}

func (d *Driver) Create(ctx context.Context, inst model.Instance, plan model.Plan) (string, error) {
	if err := d.record("Create", inst.Name); err != nil {
		return "", err
	}
	c := host.Container{
		ID:       "c0ffee" + inst.Name,
		Name:     host.ContainerName(inst.Name),
		Instance: inst.Name,
		Game:     inst.Game,
		PlanHash: host.PlanHash(plan),
		State:    model.StateCreated,
		Ports:    plan.Ports,
		Health:   model.Health{OK: true},
	}
	d.Put(c)
	return c.ID, nil
}

func (d *Driver) Start(ctx context.Context, id string) error {
	if err := d.record("Start", id); err != nil {
		return err
	}
	// Read the clock before transition takes the lock: sync.Mutex is not
	// reentrant, and calling now() inside the closure deadlocks.
	started := d.now()
	return d.transition(id, func(c *host.Container) {
		c.State = model.StateRunning
		c.Detail = ""
		c.ExitCode = 0
		c.Started = started
		// A container with no healthcheck declared reports healthy, which
		// is what the real driver does: absence of a check is not evidence
		// of ill health. A fake that left this false would make every
		// healthcheck step time out.
		c.Health = model.Health{OK: true}
	})
}

func (d *Driver) Stop(ctx context.Context, id, signal string, grace time.Duration) error {
	if err := d.record("Stop", id, signal, grace.String()); err != nil {
		return err
	}
	return d.transition(id, func(c *host.Container) {
		c.State = model.StateStopped
		c.Detail = ""
		c.ExitCode = 0
		c.Started = time.Time{}
	})
}

func (d *Driver) Remove(ctx context.Context, id string, withVolumes bool) error {
	if err := d.record("Remove", id); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.containers, id)
	for i, existing := range d.order {
		if existing == id {
			d.order = append(d.order[:i], d.order[i+1:]...)
			break
		}
	}
	return nil
}

func (d *Driver) Stats(ctx context.Context, id string) (<-chan host.Sample, error) {
	if err := d.record("Stats", id); err != nil {
		return nil, err
	}
	d.mu.Lock()
	samples := append([]host.Sample(nil), d.samples...)
	d.mu.Unlock()

	out := make(chan host.Sample)
	go func() {
		defer close(out)
		for _, s := range samples {
			select {
			case out <- s:
			case <-ctx.Done():
				return
			}
		}
		<-ctx.Done()
	}()
	return out, nil
}

func (d *Driver) Logs(ctx context.Context, id string, tail int) (io.ReadCloser, error) {
	if err := d.record("Logs", id); err != nil {
		return nil, err
	}
	d.mu.Lock()
	text := d.logText
	d.mu.Unlock()

	return &followReader{r: strings.NewReader(text), done: make(chan struct{})}, nil
}

// followReader models `docker logs --follow`: it yields the canned output and
// then blocks rather than reporting EOF, because a real follow does not end
// while the container is running.
//
// The distinction matters. A reader that EOFs immediately makes a consumer
// look like it is reconnecting in a loop, which is exactly the failure the
// stream supervisor is built to avoid — a fake that ends every stream would
// make that bug untestable and would fail the tests that check for it.
type followReader struct {
	r        *strings.Reader
	done     chan struct{}
	closeOne sync.Once
}

func (f *followReader) Read(p []byte) (int, error) {
	if f.r.Len() > 0 {
		return f.r.Read(p)
	}
	<-f.done
	return 0, io.EOF
}

func (f *followReader) Close() error {
	f.closeOne.Do(func() { close(f.done) })
	return nil
}

func (d *Driver) Exec(ctx context.Context, id string, argv []string) ([]byte, error) {
	if err := d.record("Exec", id, strings.Join(argv, " ")); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.execOut[strings.Join(argv, " ")], nil
}

func (d *Driver) transition(id string, f func(*host.Container)) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.containers[id]
	if !ok {
		return fmt.Errorf("no such container: %s", id)
	}
	f(&c)
	d.containers[id] = c
	return nil
}
