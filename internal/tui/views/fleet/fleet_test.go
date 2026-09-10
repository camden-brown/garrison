package fleet_test

import (
	"errors"
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
	"github.com/camden-brown/garrison/internal/tasks"
	"github.com/camden-brown/garrison/internal/tui"
	"github.com/camden-brown/garrison/internal/tui/comp"
	"github.com/camden-brown/garrison/internal/tui/views/fleet"
)

var update = flag.Bool("update", false, "rewrite the .golden files")

// The colour profile is pinned for every render test. Without it the output
// depends on the terminal the suite happens to run in, and CI and a laptop
// disagree for reasons that have nothing to do with the code.
func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii)
	// Timestamps render in the operator's zone, so the goldens need one
	// pinned or they move with whoever runs the suite.
	time.Local = time.UTC
	os.Exit(m.Run())
}

var now = time.Date(2026, 9, 9, 21, 7, 0, 0, time.UTC)

func containers() []host.Container {
	return []host.Container{
		{
			Instance: "zomboid-main",
			Game:     "zomboid",
			State:    model.StateRunning,
			Started:  now.Add(-6*24*time.Hour - 4*time.Hour),
			Ports:    []model.PortMap{{Container: "16261/udp", Host: 16261}},
			Health:   model.Health{OK: true},
		},
		{
			Instance: "valheim-huldra",
			Game:     "valheim",
			State:    model.StateRunning,
			Started:  now.Add(-2*24*time.Hour - 11*time.Hour),
			Ports:    []model.PortMap{{Container: "2456/udp", Host: 2456}},
			Health:   model.Health{OK: true},
		},
		{
			Instance: "palworld-sat",
			Game:     "palworld",
			State:    model.StateCrashed,
			Detail:   "OOM killed",
			ExitCode: 137,
			Restarts: 3,
		},
		{
			Instance: "zomboid-testing",
			Game:     "zomboid",
			State:    model.StateStopped,
			Detail:   "exit 0",
		},
	}
}

func snapshot(ms ...core.Mutation) core.Snapshot {
	s := core.Snapshot{Engine: core.Engine{Transport: "npipe"}}
	all := append([]core.Mutation{core.FleetObserved{At: now, Containers: containers()}}, ms...)
	return core.Reduce(s, all...)
}

func render(t *testing.T, v tui.View, snap core.Snapshot, width, height int) string {
	t.Helper()
	return v.Render(tui.Frame{
		Width:  width,
		Height: height,
		Theme:  comp.NewTheme(false),
		Now:    now,
	}, snap)
}

// The only cheap thing that catches "a long server name pushes the note column
// off screen", which is the bug class that makes a dashboard unreadable at
// exactly the moment it matters.
func TestGoldenRenders(t *testing.T) {
	tests := []struct {
		name   string
		width  int
		height int
		snap   core.Snapshot
		setup  func(tui.View, core.Snapshot) tui.View
	}{
		{name: "wide", width: 92, height: 34, snap: snapshot()},
		{name: "narrow", width: 80, height: 24, snap: snapshot()},
		{name: "no-rail-wide", width: 120, height: 34, snap: snapshot()},
		{
			// The activity feed across a day boundary. A crash at 21:07 is a
			// different story depending on which evening it was, so the feed
			// says — and every time here is stored UTC and rendered local.
			name: "activity-across-days", width: 120, height: 34,
			snap: snapshot(core.LogEventsRead{At: now, Server: "zomboid-main", Events: []model.Event{
				{Kind: model.KindError, At: now.AddDate(0, 0, -2), Text: "mod exploded"},
				{Kind: model.KindJoin, At: now.AddDate(0, 0, -1), Player: "Huldra", Text: "Huldra joined"},
				{Kind: model.KindChat, At: now.Add(-3 * time.Hour), Text: "<Huldra> anyone seen the boat"},
				{Kind: model.KindLeave, At: now.Add(-20 * time.Minute), Text: "Huldra left"},
			}}),
		},
		{
			name: "engine-unreachable", width: 92, height: 34,
			snap: snapshot(core.FleetUnobservable{
				At:  now,
				Err: errors.New("cannot connect to the Docker daemon"),
			}),
		},
		{
			name: "empty", width: 92, height: 34,
			snap: func() core.Snapshot {
				return core.Reduce(
					core.Snapshot{Engine: core.Engine{Transport: "npipe", OK: true}},
					core.FleetObserved{At: now},
				)
			}(),
		},
		{
			name: "operation-in-flight", width: 92, height: 34,
			snap: snapshot(taskRunning("palworld-sat", tasks.KindStart, 0)),
		},
		{
			// A stop is a wait, not an instant, so the row says how long.
			name: "stopping-with-grace", width: 92, height: 34,
			snap: snapshot(taskRunning("zomboid-main", tasks.KindStop, 60*time.Second)),
		},
		{
			// A stop Garrison asked for that had to be forced: the glyph
			// stays grey because the state is honest, the reason stays red
			// because a server killed mid-write is a save you may not have.
			name: "stopped-after-a-kill", width: 92, height: 34,
			snap: snapshot(
				taskRunning("zomboid-main", tasks.KindStop, 60*time.Second),
				taskDone("zomboid-main", tasks.KindStop),
				core.FleetObserved{At: now, Containers: []host.Container{{
					Instance: "zomboid-main",
					Game:     "zomboid",
					State:    model.StateCrashed,
					ExitCode: 137,
				}}},
			),
		},
		{
			name: "failed-operation", width: 92, height: 34,
			snap: snapshot(core.TaskProgressed{At: now, Progress: tasks.Progress{
				ID: "start-1", Server: "zomboid-testing", Kind: tasks.KindStart,
				State: tasks.StateFailed,
				Err:   "zomboid-testing: start: port 16261 already allocated",
			}}),
		},
		{
			name: "confirming-stop", width: 92, height: 34,
			snap: snapshot(),
			setup: func(v tui.View, snap core.Snapshot) tui.View {
				next, _ := v.Update(keyPress("S"), frameFor(snap), snap)
				return next
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v tui.View = fleet.New()
			if tt.setup != nil {
				v = tt.setup(v, tt.snap)
			}

			got := render(t, v, tt.snap, tt.width, tt.height)
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

// No line may exceed the terminal. A row that wraps costs two lines and pushes
// the bottom of the fleet off the screen.
func TestNoLineExceedsTheFrame(t *testing.T) {
	longName := host.Container{
		Instance: "a-server-with-a-truly-unreasonable-name-that-nobody-would-choose",
		Game:     "some-game-with-a-long-id",
		State:    model.StateCrashed,
		Detail:   "exit 137: the container ran out of memory and was killed by the kernel",
	}

	snap := core.Reduce(core.Snapshot{Engine: core.Engine{OK: true}},
		core.FleetObserved{At: now, Containers: append(containers(), longName)})

	for _, width := range []int{80, 100, 120, 200} {
		v := fleet.New()
		out := render(t, v, snap, width, 34)
		for i, line := range splitLines(out) {
			if w := lipgloss.Width(line); w > width {
				t.Errorf("width %d: line %d is %d cells:\n%s", width, i, w, line)
			}
		}
	}
}

func TestStartAndStopEmitActions(t *testing.T) {
	snap := snapshot()

	t.Run("start is immediate", func(t *testing.T) {
		v := fleet.New()
		_, cmd := v.Update(keyPress("u"), frameFor(snap), snap)
		if cmd == nil {
			t.Fatal("u produced no command")
		}
		msg, ok := cmd().(tui.ActionMsg)
		if !ok {
			t.Fatalf("got %T, want tui.ActionMsg", cmd())
		}
		// Servers sort by name, so the cursor starts on palworld-sat.
		if msg.Op != core.OpStart || msg.Server != "palworld-sat" {
			t.Errorf("action = %+v", msg)
		}
	})

	// Uppercase confirms. Pressing it must not act on its own.
	t.Run("stop confirms first", func(t *testing.T) {
		v := fleet.New()
		next, cmd := v.Update(keyPress("S"), frameFor(snap), snap)
		if cmd != nil {
			t.Error("S acted without confirmation")
		}

		after, cmd := next.Update(keyPress("y"), frameFor(snap), snap)
		if cmd == nil {
			t.Fatal("y produced no command")
		}
		msg := cmd().(tui.ActionMsg)
		if msg.Op != core.OpStop || msg.Server != "palworld-sat" {
			t.Errorf("action = %+v", msg)
		}
		_ = after
	})

	t.Run("cancelling drops the confirmation", func(t *testing.T) {
		v := fleet.New()
		next, _ := v.Update(keyPress("S"), frameFor(snap), snap)
		after, cmd := next.Update(keyPress("n"), frameFor(snap), snap)
		if cmd != nil {
			t.Error("n produced a command")
		}
		if got := render(t, after, snap, 120, 34); contains(got, "y / n") {
			t.Error("the confirmation is still on screen after cancelling")
		}
	})
}

func TestCursorStaysInRangeWhenTheFleetShrinks(t *testing.T) {
	full := snapshot()

	var v tui.View = fleet.New()
	for i := 0; i < 10; i++ {
		v, _ = v.Update(keyPress("j"), frameFor(full), full)
	}

	// Every server but one disappears between polls.
	shrunk := core.Reduce(core.Snapshot{Engine: core.Engine{OK: true}},
		core.FleetObserved{At: now, Containers: containers()[:1]})

	v, cmd := v.Update(keyPress("u"), frameFor(shrunk), shrunk)
	if cmd == nil {
		t.Fatal("no command after the fleet shrank — the cursor is out of range")
	}
	if msg := cmd().(tui.ActionMsg); msg.Server != "zomboid-main" {
		t.Errorf("acted on %q, want the only remaining server", msg.Server)
	}
	_ = v
}

// --- helpers ---

func keyPress(s string) tea.KeyMsg {
	if len(s) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func splitLines(s string) []string { return strings.Split(s, "\n") }

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

// frameFor is what the shell would hand the view: the first server selected,
// which is what the rail defaults to.
func frameFor(snap core.Snapshot) tui.Frame {
	f := tui.Frame{Width: 120, Height: 34, Theme: comp.NewTheme(false), Now: now, Focused: true}
	if len(snap.Servers) > 0 {
		f.Server = snap.Servers[0].Name
	}
	return f
}

// taskRunning is a task in flight, with the grace its stop step revised to.
func taskRunning(server string, kind tasks.Kind, grace time.Duration) core.TaskProgressed {
	return core.TaskProgressed{At: now, Progress: tasks.Progress{
		ID: string(kind) + "-1", Server: server, Kind: kind,
		State: tasks.StateRunning, Steps: []string{"stop"},
		Est: []time.Duration{grace},
	}}
}

func taskDone(server string, kind tasks.Kind) core.TaskProgressed {
	return core.TaskProgressed{At: now, Progress: tasks.Progress{
		ID: string(kind) + "-1", Server: server, Kind: kind,
		State: tasks.StateDone, Steps: []string{"stop"}, Cursor: 1,
	}}
}

// "/" narrows the table. It is one of the three things a view owns, and the
// fleet is where it matters most: the screen you look at when something is
// wrong is also the one with the most rows.
func TestFilterNarrowsTheTable(t *testing.T) {
	snap := snapshot()

	v := pressFleet(t, fleet.New(), snap, "/")
	v = pressFleet(t, v, snap, "a")

	got := v.Render(fleetFrame(), snap)
	if !strings.Contains(got, "a") {
		t.Errorf("the matching server is gone:\n%s", got)
	}
}

// While the bar is open every key is text, or typing a server's name presses
// S on the "s" and stops one.
func TestFilterSwallowsTheDestructiveKeys(t *testing.T) {
	snap := snapshot()

	v := pressFleet(t, fleet.New(), snap, "/")
	v = pressFleet(t, v, snap, "S")

	if got := v.Render(fleetFrame(), snap); strings.Contains(got, "Stop") || strings.Contains(got, "type") {
		t.Errorf("typing into the filter opened a confirmation:\n%s", got)
	}
}

func TestFilterEscapeRestoresTheTable(t *testing.T) {
	snap := snapshot()
	before := fleet.New().Render(fleetFrame(), snap)

	v := pressFleet(t, fleet.New(), snap, "/")
	v = pressFleet(t, v, snap, "z", "z", "z")
	v = pressFleet(t, v, snap, "esc")

	if got := v.Render(fleetFrame(), snap); got != before {
		t.Errorf("esc did not restore the unfiltered table:\n%s", got)
	}
}

func TestFilterWithNoMatchesExplainsItself(t *testing.T) {
	snap := snapshot()

	v := pressFleet(t, fleet.New(), snap, "/")
	v = pressFleet(t, v, snap, "z", "z", "z")

	if got := v.Render(fleetFrame(), snap); !strings.Contains(got, "Nothing matching") {
		t.Errorf("an empty result does not explain itself:\n%s", got)
	}
}

// fleetFrame is the frame the filter tests render into.
func fleetFrame() tui.Frame {
	return tui.Frame{Width: 120, Height: 34, Theme: comp.NewTheme(false), Now: now, Focused: true}
}

// pressFleet drives keys the way the shell does.
func pressFleet(t *testing.T, v tui.View, snap core.Snapshot, keys ...string) tui.View {
	t.Helper()
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		v, _ = v.Update(msg, fleetFrame(), snap)
	}
	return v
}
