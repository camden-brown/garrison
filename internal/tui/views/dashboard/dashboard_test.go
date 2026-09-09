package dashboard_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tui"
	"github.com/camden-brown/garrison/internal/tui/comp"
	"github.com/camden-brown/garrison/internal/tui/views/dashboard"
)

var update = flag.Bool("update", false, "rewrite the .golden files")

func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii)
	os.Exit(m.Run())
}

var now = time.Date(2026, 9, 9, 21, 7, 0, 0, time.UTC)

// A snapshot with a minute of plausible history, so the sparklines have
// something to draw and the golden catches a change in how they are drawn.
func populated() core.Snapshot {
	s := core.Reduce(
		core.Snapshot{Engine: core.Engine{Transport: "npipe", OK: true}},
		core.FleetObserved{At: now, MetricLabels: map[string]string{"valheim": "world save"}, Containers: []host.Container{
			{
				Instance: "valheim-huldra",
				Game:     "valheim",
				State:    model.StateRunning,
				Started:  now.Add(-59 * time.Hour),
				Health:   model.Health{OK: true},
			},
			{Instance: "zomboid-main", Game: "zomboid", State: model.StateStopped},
		}},
	)

	// A player, and a console with a repeated line to collapse.
	s = core.Reduce(s, core.LogEventsRead{
		At:     now,
		Server: "valheim-huldra",
		Events: []model.Event{
			{Kind: model.KindConnect, At: now, SteamID: "76561190000000001"},
			{Kind: model.KindJoin, At: now, Player: "Dalinar", Text: "Dalinar joined"},
			{Kind: model.KindSave, At: now, Text: "world saved in 314ms", Metric: "world_save_ms", Value: 314},
			{Kind: model.KindWarn, At: now, Text: "a mod is unhappy"},
			{Kind: model.KindWarn, At: now, Text: "a mod is unhappy"},
			{Kind: model.KindWarn, At: now, Text: "a mod is unhappy"},
			{Kind: model.KindInfo, At: now, Text: "server ready"},
		},
	})

	// A ramp plus a spike, which is what makes a sparkline worth having.
	for i := 0; i < 60; i++ {
		cpu := 8 + float64(i%7)
		if i == 45 {
			cpu = 82
		}
		s = core.Reduce(s, core.StatsSampled{
			At:     now.Add(time.Duration(i-60) * time.Second),
			Server: "valheim-huldra",
			Sample: host.Sample{
				At:         now.Add(time.Duration(i-60) * time.Second),
				CPUPct:     cpu,
				MemBytes:   int64(3<<30) + int64(i)<<24,
				MemLimit:   8 << 30,
				NetRxBytes: int64(i) * 40_000,
				NetTxBytes: int64(i) * 12_000,
			},
		})
	}
	return s
}

func render(t *testing.T, v tui.View, snap core.Snapshot, w, h int) string {
	t.Helper()
	return v.Render(tui.Frame{Width: w, Height: h, Theme: comp.NewTheme(false), Now: now}, snap)
}

func TestGoldenRenders(t *testing.T) {
	tests := []struct {
		name   string
		width  int
		height int
		snap   core.Snapshot
	}{
		{name: "wide", width: 120, height: 34, snap: populated()},
		{name: "narrow", width: 80, height: 24, snap: populated()},
		// Under 30 rows the tile strip becomes one line of inline values —
		// a real narrow layout, not a clipped wide one.
		{name: "short", width: 120, height: 10, snap: populated()},
		{
			name: "no-history-yet", width: 120, height: 34,
			snap: core.Reduce(core.Snapshot{Engine: core.Engine{OK: true}},
				core.FleetObserved{At: now, Containers: []host.Container{
					{Instance: "valheim-huldra", Game: "valheim", State: model.StateRunning},
				}}),
		},
		{
			name: "empty-fleet", width: 120, height: 34,
			snap: core.Snapshot{Engine: core.Engine{OK: true}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := render(t, dashboard.New(), tt.snap, tt.width, tt.height)
			golden := filepath.Join("testdata", tt.name+".golden")

			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatalf("writing golden: %v", err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("reading golden (run with -update to create): %v", err)
			}
			if got != string(want) {
				t.Errorf("render differs from %s\n--- got ---\n%s\n--- want ---\n%s", golden, got, want)
			}
		})
	}
}

func TestNoLineExceedsTheFrame(t *testing.T) {
	for _, width := range []int{80, 100, 120, 200} {
		out := render(t, dashboard.New(), populated(), width, 34)
		for i, line := range strings.Split(out, "\n") {
			if w := lipgloss.Width(line); w > width {
				t.Errorf("width %d: line %d is %d cells:\n%s", width, i, w, line)
			}
		}
	}
}

// The fleet changes every five seconds. A cursor left pointing past the end
// would panic on the next render.
func TestCursorSurvivesTheFleetShrinking(t *testing.T) {
	var v tui.View = dashboard.New()
	full := populated()

	for i := 0; i < 5; i++ {
		v, _ = v.Update(tea.KeyMsg{Type: tea.KeyRight}, frameFor(full), full)
	}

	shrunk := core.Reduce(core.Snapshot{Engine: core.Engine{OK: true}},
		core.FleetObserved{At: now, Containers: []host.Container{
			{Instance: "only", Game: "valheim", State: model.StateRunning},
		}})

	out := render(t, v, shrunk, 120, 34)
	if !strings.Contains(out, "only") {
		t.Errorf("render after the fleet shrank:\n%s", out)
	}
}

// An empty fleet must explain itself rather than rendering a blank screen.
func TestEmptyFleetSaysSo(t *testing.T) {
	out := render(t, dashboard.New(), core.Snapshot{Engine: core.Engine{OK: true}}, 120, 34)
	if strings.TrimSpace(out) == "" {
		t.Error("an empty fleet rendered nothing at all")
	}
}

func frameFor(snap core.Snapshot) tui.Frame {
	f := tui.Frame{Width: 92, Height: 34, Theme: comp.NewTheme(false), Now: now, Focused: true}
	if len(snap.Servers) > 0 {
		f.Server = snap.Servers[0].Name
	}
	return f
}
