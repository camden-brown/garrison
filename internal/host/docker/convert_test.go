package docker

import (
	"testing"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"

	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/model"
)

// The distinction this table exists for: a clean exit is "stopped", anything
// else is "crashed". Getting it wrong paints a dead server grey and nobody
// looks at it again.
func TestStateFrom(t *testing.T) {
	tests := []struct {
		name       string
		state      types.ContainerState
		want       model.State
		wantDetail string
	}{
		{
			name:  "running",
			state: types.ContainerState{Status: "running", Running: true},
			want:  model.StateRunning,
		},
		{
			name:  "clean exit is stopped, not crashed",
			state: types.ContainerState{Status: "exited", ExitCode: 0},
			want:  model.StateStopped,
		},
		{
			name:       "non-zero exit is a crash and carries the code",
			state:      types.ContainerState{Status: "exited", ExitCode: 137},
			want:       model.StateCrashed,
			wantDetail: "exit 137",
		},
		{
			name:       "exit error is appended when the engine gives one",
			state:      types.ContainerState{Status: "exited", ExitCode: 1, Error: "no space left on device"},
			want:       model.StateCrashed,
			wantDetail: "exit 1: no space left on device",
		},
		{
			name:       "OOM outranks the exit code, because the code does not explain it",
			state:      types.ContainerState{Status: "exited", ExitCode: 137, OOMKilled: true},
			want:       model.StateCrashed,
			wantDetail: "OOM killed",
		},
		{
			name:  "restarting is transitional",
			state: types.ContainerState{Status: "restarting", Restarting: true},
			want:  model.StateRestarting,
		},
		{
			name:       "paused has no glyph of its own and says why",
			state:      types.ContainerState{Status: "paused", Paused: true},
			want:       model.StateStopped,
			wantDetail: "paused",
		},
		{
			name:  "created",
			state: types.ContainerState{Status: "created"},
			want:  model.StateCreated,
		},
		{
			name:       "dead",
			state:      types.ContainerState{Status: "dead", Dead: true},
			want:       model.StateCrashed,
			wantDetail: "dead",
		},
		{
			name:  "an empty status is unknown, not stopped",
			state: types.ContainerState{},
			want:  model.StateUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, detail := stateFrom(tt.state)
			if got != tt.want {
				t.Errorf("state = %v, want %v", got, tt.want)
			}
			if detail != tt.wantDetail {
				t.Errorf("detail = %q, want %q", detail, tt.wantDetail)
			}
		})
	}
}

func TestHealthFrom(t *testing.T) {
	checked := time.Date(2026, 9, 9, 21, 4, 0, 0, time.UTC)

	t.Run("no healthcheck declared is not ill health", func(t *testing.T) {
		if got := healthFrom(nil); !got.OK {
			t.Errorf("healthFrom(nil).OK = false, want true")
		}
	})

	t.Run("starting is not yet unhealthy", func(t *testing.T) {
		got := healthFrom(&types.Health{Status: types.Starting})
		if !got.OK || got.Detail != "" {
			t.Errorf("healthFrom(starting) = %+v, want OK with no detail", got)
		}
	})

	t.Run("unhealthy carries the first line of the probe output", func(t *testing.T) {
		got := healthFrom(&types.Health{
			Status: types.Unhealthy,
			Log: []*types.HealthcheckResult{{
				End:    checked,
				Output: "connection refused\nstack trace follows\n",
			}},
		})
		if got.OK {
			t.Error("OK = true, want false")
		}
		if got.Detail != "connection refused" {
			t.Errorf("Detail = %q, want %q", got.Detail, "connection refused")
		}
		if !got.CheckedAt.Equal(checked) {
			t.Errorf("CheckedAt = %v, want %v", got.CheckedAt, checked)
		}
	})
}

func TestPortsFromIsSortedAndSkipsUnpublished(t *testing.T) {
	got := portsFrom(nat.PortMap{
		"16262/udp": {{HostPort: "16262"}},
		"16261/udp": {{HostPort: "16261"}},
		"8766/tcp":  nil, // exposed but not published
	})

	want := []model.PortMap{
		{Container: "16261/udp", Host: 16261},
		{Container: "16262/udp", Host: 16262},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d ports %v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("port %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestContainerFromFallsBackToTheContainerName(t *testing.T) {
	// A container labelled managed but missing its instance label is still
	// ours. Hiding it would be worse than naming it awkwardly.
	got := containerFrom(types.ContainerJSON{
		ContainerJSONBase: &types.ContainerJSONBase{
			ID:   "abc123",
			Name: "/garrison-zomboid-main",
		},
		Config: &container.Config{Labels: map[string]string{host.LabelManaged: "1"}},
	})

	if got.Instance != "zomboid-main" {
		t.Errorf("Instance = %q, want %q", got.Instance, "zomboid-main")
	}
}

func TestParseTimeTreatsDockersZeroAsZero(t *testing.T) {
	if got := parseTime("0001-01-01T00:00:00Z"); !got.IsZero() {
		t.Errorf("parseTime(docker zero) = %v, want the zero time", got)
	}
	if got := parseTime(""); !got.IsZero() {
		t.Errorf("parseTime(\"\") = %v, want the zero time", got)
	}
}
