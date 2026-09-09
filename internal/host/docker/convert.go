package docker

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/go-connections/nat"

	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/model"
)

// containerFrom turns a Docker inspect response into the shape the rest of
// Garrison speaks.
//
// It is a plain function over plain data so the interesting cases — a
// non-zero exit is a crash, an OOM kill says so, a missing label falls back to
// the container name — are table tests rather than something you find out
// about when a server dies at 2am.
func containerFrom(j types.ContainerJSON) host.Container {
	c := host.Container{ID: j.ID}
	if j.ContainerJSONBase != nil {
		c.Name = strings.TrimPrefix(j.Name, "/")
		c.Restarts = j.RestartCount
	}
	if j.Config != nil {
		c.Instance = j.Config.Labels[host.LabelInstance]
		c.Game = j.Config.Labels[host.LabelGame]
		c.PlanHash = j.Config.Labels[host.LabelPlanHash]
	}
	if c.Instance == "" {
		// A container labelled managed but missing its instance label is
		// still ours; naming it after the container beats hiding it.
		c.Instance = strings.TrimPrefix(c.Name, host.NamePrefix)
	}

	if j.State != nil {
		c.State, c.Detail = stateFrom(*j.State)
		c.ExitCode = j.State.ExitCode
		c.Started = parseTime(j.State.StartedAt)
		c.Health = healthFrom(j.State.Health)
		if c.Detail == "" && c.Health.Detail != "" {
			c.Detail = c.Health.Detail
		}
	}
	if j.NetworkSettings != nil {
		c.Ports = portsFrom(j.NetworkSettings.Ports)
	}
	return c
}

// stateFrom collapses Docker's seven states onto the six the UI encodes, and
// says why when the state alone would not.
//
//   - A non-zero exit is a crash, not a stop. "Stopped" means somebody meant
//     it; anything else is a thing you want to see in red.
//   - "paused" has no glyph of its own. It reports as stopped with the reason
//     attached rather than inventing a seventh state for something Garrison
//     never does on purpose.
//   - "removing" is transitional and reports as restarting, which is the
//     transitioning glyph.
func stateFrom(s types.ContainerState) (model.State, string) {
	switch {
	case s.OOMKilled:
		return model.StateCrashed, "OOM killed"
	case s.Restarting:
		return model.StateRestarting, ""
	case s.Paused:
		return model.StateStopped, "paused"
	case s.Dead:
		return model.StateCrashed, "dead"
	case s.Running:
		return model.StateRunning, ""
	}

	switch s.Status {
	case "created":
		return model.StateCreated, ""
	case "removing":
		return model.StateRestarting, "removing"
	case "exited":
		if s.ExitCode != 0 {
			detail := fmt.Sprintf("exit %d", s.ExitCode)
			if s.Error != "" {
				detail += ": " + s.Error
			}
			return model.StateCrashed, detail
		}
		return model.StateStopped, ""
	case "":
		return model.StateUnknown, ""
	}
	return model.StateUnknown, s.Status
}

// healthFrom reads the container healthcheck. A container with no healthcheck
// declared reports OK: absence of a check is not evidence of ill health, and
// treating it as a failure would paint every server amber.
func healthFrom(h *types.Health) model.Health {
	if h == nil || h.Status == "" || h.Status == types.NoHealthcheck {
		return model.Health{OK: true}
	}

	out := model.Health{OK: h.Status == types.Healthy}
	if n := len(h.Log); n > 0 {
		out.CheckedAt = h.Log[n-1].End
		if !out.OK {
			out.Detail = firstLine(h.Log[n-1].Output)
		}
	}
	if !out.OK && out.Detail == "" {
		out.Detail = h.Status
	}
	if h.Status == types.Starting {
		// Starting is not unhealthy yet; it is a container that has not
		// finished waking up.
		out.OK = true
		out.Detail = ""
	}
	return out
}

// portsFrom flattens Docker's port map into the published mappings, sorted so
// two observations of the same container render identically.
func portsFrom(pm nat.PortMap) []model.PortMap {
	out := make([]model.PortMap, 0, len(pm))
	for port, bindings := range pm {
		for _, b := range bindings {
			hostPort, err := strconv.Atoi(b.HostPort)
			if err != nil {
				continue
			}
			out = append(out, model.PortMap{Container: string(port), Host: hostPort})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Host != out[j].Host {
			return out[i].Host < out[j].Host
		}
		return out[i].Container < out[j].Container
	})
	return out
}

// parseTime reads Docker's timestamps. The zero timestamp Docker uses for
// "never started" comes back as a zero time.Time rather than the year 1.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() <= 1 {
		return time.Time{}
	}
	return t
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
