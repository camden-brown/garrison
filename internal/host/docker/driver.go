package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"

	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/model"
)

// Driver runs containers through the Docker Engine API.
type Driver struct {
	cli      *client.Client
	endpoint string
	origin   Origin
}

// Compile-time proof that this satisfies the interface the rest of the program
// is written against.
var _ host.Driver = (*Driver)(nil)

// Open prepares a driver for an endpoint. It does not dial.
//
// That is deliberate: Docker Desktop is often not up yet when a login-launched
// Garrison starts, and a dashboard that refuses to open because the thing it
// watches is down is a dashboard that is missing at exactly the moment you
// wanted it. An unreachable engine shows as StateUnknown across the fleet and
// recovers on its own when Ping starts succeeding.
func Open(configured string, env Env) (*Driver, error) {
	endpoint, origin, err := Resolve(configured, env)
	if err != nil {
		return nil, err
	}

	// Version negotiation happens on the first call, so it costs nothing here
	// and keeps Garrison working against an older engine than it was built
	// against.
	cli, err := client.NewClientWithOpts(
		client.WithHost(endpoint),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("docker client for %s (from %s): %w", endpoint, origin, err)
	}
	return &Driver{cli: cli, endpoint: endpoint, origin: origin}, nil
}

// Endpoint is the resolved endpoint, and where the value came from.
func (d *Driver) Endpoint() (string, Origin) { return d.endpoint, d.origin }

// Close releases the underlying HTTP transport.
func (d *Driver) Close() error { return d.cli.Close() }

func (d *Driver) Ping(ctx context.Context) error {
	if _, err := d.cli.Ping(ctx); err != nil {
		return fmt.Errorf("ping %s: %w", d.endpoint, err)
	}
	return nil
}

// Pull fetches an image.
//
// The engine streams progress as JSON lines; they are handed on as text rather
// than parsed into a percentage, because a layered pull has no single
// percentage and inventing one is worse than showing what the engine said.
func (d *Driver) Pull(ctx context.Context, ref string, progress func(string)) error {
	body, err := d.cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	defer body.Close()

	dec := json.NewDecoder(body)
	for {
		var line struct {
			Status   string `json:"status"`
			Progress string `json:"progress"`
			Error    string `json:"error"`
		}
		if err := dec.Decode(&line); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("pull %s: %w", ref, err)
		}
		if line.Error != "" {
			return fmt.Errorf("pull %s: %s", ref, line.Error)
		}
		if progress != nil && line.Status != "" {
			text := line.Status
			if line.Progress != "" {
				text += " " + line.Progress
			}
			progress(text)
		}
	}
}

// Info describes the engine's host. Called rarely — none of it changes while
// Garrison is running, except the container count, which the fleet poll
// already knows more precisely.
func (d *Driver) Info(ctx context.Context) (host.Info, error) {
	info, err := d.cli.Info(ctx)
	if err != nil {
		return host.Info{}, fmt.Errorf("engine info: %w", err)
	}
	return host.Info{
		Version:    info.ServerVersion,
		OS:         info.OperatingSystem,
		NCPU:       info.NCPU,
		MemTotal:   info.MemTotal,
		Containers: info.Containers,
	}, nil
}

// List finds every container Garrison manages by label, then inspects each
// one.
//
// The label filter rather than local bookkeeping is what makes the fleet
// rediscoverable after losing %APPDATA%. The inspect is the second call the
// list summary cannot replace: exit codes, restart counts, health and start
// times are only there, and the summary's "Exited (137) 4 days ago" is a
// display string that would have to be parsed back apart.
//
// Sequential on purpose. A fleet is single digits of containers and each
// inspect is a millisecond over a local socket; a worker pool here would buy
// nothing and would need a lock to collect into.
func (d *Driver) List(ctx context.Context) ([]host.Container, error) {
	summaries, err := d.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", host.LabelManaged+"=1")),
	})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	out := make([]host.Container, 0, len(summaries))
	for _, s := range summaries {
		j, err := d.cli.ContainerInspect(ctx, s.ID)
		if err != nil {
			if client.IsErrNotFound(err) {
				// Removed between the list and the inspect. Not an error:
				// the next poll simply will not show it.
				continue
			}
			return nil, fmt.Errorf("inspect %s: %w", short(s.ID), err)
		}
		out = append(out, containerFrom(j))
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Instance < out[j].Instance })
	return out, nil
}

func (d *Driver) Inspect(ctx context.Context, id string) (host.Container, error) {
	j, err := d.cli.ContainerInspect(ctx, id)
	if err != nil {
		return host.Container{}, fmt.Errorf("inspect %s: %w", short(id), err)
	}
	return containerFrom(j), nil
}

// Create builds the container an instance's plan describes.
//
// The plan is hashed onto the container as a label so a later reconciliation
// can tell that the config has moved on without the container. Nothing here
// interprets the plan: if a field is wrong the game that produced it is where
// the fix goes.
func (d *Driver) Create(ctx context.Context, inst model.Instance, plan model.Plan) (string, error) {
	exposed, bindings, err := portBindings(plan.Ports)
	if err != nil {
		return "", fmt.Errorf("%s: create container: %w", inst.Name, err)
	}

	cfg := &container.Config{
		Image:        plan.Image,
		Cmd:          plan.Cmd,
		Env:          envList(plan.Env),
		Labels:       labels(inst, plan),
		ExposedPorts: exposed,
		StopSignal:   plan.StopSignal,
		Healthcheck:  healthcheck(plan.Health),
	}
	if plan.StopGrace > 0 {
		secs := int(plan.StopGrace.Round(time.Second).Seconds())
		cfg.StopTimeout = &secs
	}

	hostCfg := &container.HostConfig{
		Binds:        binds(plan.Mounts),
		PortBindings: bindings,
		Resources: container.Resources{
			Memory:   plan.Resources.Memory,
			NanoCPUs: int64(plan.Resources.CPUs * 1e9),
		},
		// No restart policy: Garrison is the supervisor. Handing that to
		// Docker means a crash-looping server restarts behind Garrison's
		// back, floods the logs, and reconnects the stats stream every few
		// seconds — the amplification the design calls out. When a task
		// wants a container back up, it starts it.
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled},
	}

	created, err := d.cli.ContainerCreate(ctx, cfg, hostCfg, nil, nil, host.ContainerName(inst.Name))
	if err != nil {
		return "", fmt.Errorf("%s: create container: %w", inst.Name, err)
	}
	return created.ID, nil
}

func (d *Driver) Start(ctx context.Context, id string) error {
	if err := d.cli.ContainerStart(ctx, id, container.StartOptions{}); err != nil {
		return fmt.Errorf("start %s: %w", short(id), err)
	}
	return nil
}

// Stop sends the game's own stop signal and waits out its grace period. Both
// come from the plan because they are game facts: Valheim's image traps SIGINT
// to save the world, and a Zomboid server needs two minutes to finish writing.
func (d *Driver) Stop(ctx context.Context, id, signal string, grace time.Duration) error {
	opts := container.StopOptions{Signal: signal}
	if grace > 0 {
		secs := int(grace.Round(time.Second).Seconds())
		opts.Timeout = &secs
	}
	if err := d.cli.ContainerStop(ctx, id, opts); err != nil {
		return fmt.Errorf("stop %s: %w", short(id), err)
	}
	return nil
}

func (d *Driver) Remove(ctx context.Context, id string, withVolumes bool) error {
	err := d.cli.ContainerRemove(ctx, id, container.RemoveOptions{RemoveVolumes: withVolumes})
	if err != nil {
		return fmt.Errorf("remove %s: %w", short(id), err)
	}
	return nil
}

// Stats decodes the engine's stats stream into samples until ctx is cancelled.
//
// The channel is unbuffered, so a stalled consumer applies backpressure all
// the way to the socket rather than letting samples pile up in a buffer. The
// send selects on ctx.Done, which is the goroutine's only other exit: cancel
// the context and the HTTP body read fails, the decoder errors, and the
// goroutine returns. A caller that neither reads nor cancels leaks it — which
// is the contract, because the alternative is a stats stream that silently
// stops reporting.
func (d *Driver) Stats(ctx context.Context, id string) (<-chan host.Sample, error) {
	resp, err := d.cli.ContainerStats(ctx, id, true)
	if err != nil {
		return nil, fmt.Errorf("stats %s: %w", short(id), err)
	}

	out := make(chan host.Sample)
	go func() {
		defer close(out)
		defer resp.Body.Close()

		dec := json.NewDecoder(resp.Body)
		for {
			var frame container.StatsResponse
			if err := dec.Decode(&frame); err != nil {
				return // stream ended, or ctx cancelled out from under it
			}
			select {
			case out <- sampleFrom(frame, time.Now()):
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// Logs follows a container's output as plain lines.
//
// Docker frames stdout and stderr into one stream unless the container has a
// TTY, in which case it does not — and demultiplexing a stream that is not
// multiplexed produces a console full of shredded bytes. So it asks first.
func (d *Driver) Logs(ctx context.Context, id string, tail int) (io.ReadCloser, error) {
	j, err := d.cli.ContainerInspect(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("logs %s: inspect for tty: %w", short(id), err)
	}

	since := "all"
	if tail >= 0 {
		since = strconv.Itoa(tail)
	}
	body, err := d.cli.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Tail:       since,
	})
	if err != nil {
		return nil, fmt.Errorf("logs %s: %w", short(id), err)
	}
	if j.Config != nil && j.Config.Tty {
		return body, nil
	}

	pr, pw := io.Pipe()
	go func() {
		defer body.Close()
		_, err := stdcopy.StdCopy(pw, pw, body)
		pw.CloseWithError(err)
	}()
	return pr, nil
}

// Exec runs a command in a container and returns its combined output. A
// non-zero exit is an error, with the output as the message: a caller that
// wanted the output of a failing command would have to check twice otherwise,
// and the one time it forgets is the time the command failed.
func (d *Driver) Exec(ctx context.Context, id string, argv []string) ([]byte, error) {
	created, err := d.cli.ContainerExecCreate(ctx, id, container.ExecOptions{
		Cmd:          argv,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return nil, fmt.Errorf("exec %s %q: %w", short(id), argv, err)
	}

	att, err := d.cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, fmt.Errorf("exec %s %q: attach: %w", short(id), argv, err)
	}
	defer att.Close()

	var buf bytes.Buffer
	if _, err := stdcopy.StdCopy(&buf, &buf, att.Reader); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("exec %s %q: read: %w", short(id), argv, err)
	}

	insp, err := d.cli.ContainerExecInspect(ctx, created.ID)
	if err != nil {
		return buf.Bytes(), fmt.Errorf("exec %s %q: inspect: %w", short(id), argv, err)
	}
	if insp.ExitCode != 0 {
		return buf.Bytes(), fmt.Errorf("exec %s %q: exit %d: %s",
			short(id), argv, insp.ExitCode, firstLine(buf.String()))
	}
	return buf.Bytes(), nil
}

// labels stamp identity and the plan hash onto the container. They are the
// only record Garrison needs to find its fleet again after losing its config
// directory, so they are set here and never by a caller.
func labels(inst model.Instance, plan model.Plan) map[string]string {
	out := make(map[string]string, len(plan.Labels)+4)
	for k, v := range plan.Labels {
		out[k] = v
	}
	out[host.LabelManaged] = "1"
	out[host.LabelInstance] = inst.Name
	out[host.LabelGame] = inst.Game
	out[host.LabelPlanHash] = host.PlanHash(plan)
	return out
}

// envList flattens the plan's environment. Sorted, because an unsorted map
// would give the same plan a different container config on every create and
// make a diff of two creates unreadable.
func envList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

func binds(mounts []model.Mount) []string {
	out := make([]string, 0, len(mounts))
	for _, m := range mounts {
		bind := m.Host + ":" + m.Container
		if m.ReadOnly {
			bind += ":ro"
		}
		out = append(out, bind)
	}
	return out
}

// portBindings converts the plan's ports. Plan.PortMap.Container already
// carries the protocol ("16261/udp") because a game server that maps the TCP
// port instead of the UDP one looks completely healthy and accepts nobody.
func portBindings(ports []model.PortMap) (nat.PortSet, nat.PortMap, error) {
	exposed := make(nat.PortSet, len(ports))
	bindings := make(nat.PortMap, len(ports))
	for _, p := range ports {
		port, err := nat.NewPort(protoOf(p.Container), portOf(p.Container))
		if err != nil {
			return nil, nil, fmt.Errorf("port %q: %w", p.Container, err)
		}
		exposed[port] = struct{}{}
		bindings[port] = append(bindings[port], nat.PortBinding{
			HostPort: strconv.Itoa(p.Host),
		})
	}
	return exposed, bindings, nil
}

// protoOf and portOf split "16261/udp". A bare "16261" is tcp, matching
// Docker's own default.
func protoOf(spec string) string {
	if _, proto, ok := strings.Cut(spec, "/"); ok {
		return proto
	}
	return "tcp"
}

func portOf(spec string) string {
	port, _, _ := strings.Cut(spec, "/")
	return port
}

func healthcheck(h *model.HealthCheck) *container.HealthConfig {
	if h == nil {
		return nil
	}
	return &container.HealthConfig{
		Test:     h.Test,
		Interval: h.Interval,
		Timeout:  h.Timeout,
		Retries:  h.Retries,
	}
}

// short trims a container ID to the twelve characters `docker ps` shows, so an
// error message is readable and greppable against it.
func short(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
