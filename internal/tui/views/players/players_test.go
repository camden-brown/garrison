package players_test

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
	"github.com/camden-brown/garrison/internal/tui/views/players"

	_ "github.com/camden-brown/garrison/internal/games/all"
)

var update = flag.Bool("update", false, "rewrite the .golden files")

func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii)
	// Occupancy buckets by local hour, so the goldens need a pinned zone or
	// they move with whoever runs the suite.
	time.Local = time.UTC
	os.Exit(m.Run())
}

var now = time.Date(2026, 9, 9, 21, 7, 0, 0, time.UTC)

func session(player string, startHour, hours int, daysAgo int) model.Session {
	joined := time.Date(2026, 9, 9-daysAgo, startHour, 0, 0, 0, time.UTC)
	return model.Session{
		Server: "valheim-huldra", Player: player, SteamID: "7656" + player,
		Joined: joined, Left: joined.Add(time.Duration(hours) * time.Hour),
	}
}

func snapshot(roster []model.Player, sessions ...model.Session) core.Snapshot {
	inst := model.Instance{Name: "valheim-huldra", Game: "valheim"}
	s := core.Reduce(core.Snapshot{Engine: core.Engine{OK: true}},
		core.InstancesLoaded{At: now, Instances: []model.Instance{inst}},
		core.FleetObserved{At: now, Containers: []host.Container{
			{Instance: "valheim-huldra", Game: "valheim", State: model.StateRunning},
		}},
	)
	if len(sessions) > 0 {
		s = core.Reduce(s, core.SessionsListed{At: now, Server: "valheim-huldra", Sessions: sessions})
	}
	// The roster is reconstructed from log events rather than set directly,
	// which is the only way it ever arrives — building it here the same way
	// keeps the test honest about where it comes from.
	if len(roster) > 0 {
		events := make([]model.Event, 0, len(roster))
		for _, p := range roster {
			events = append(events, model.Event{
				Kind: model.KindJoin, At: p.Since, Player: p.Name, SteamID: p.SteamID,
			})
		}
		s = core.Reduce(s, core.LogEventsRead{At: now, Server: "valheim-huldra", Events: events})
	}
	return s
}

func frame() tui.Frame {
	return tui.Frame{
		Width: 92, Height: 30, Theme: comp.NewTheme(false), Now: now,
		Server: "valheim-huldra", Focused: true,
	}
}

func TestNeedsAServer(t *testing.T) {
	v := players.New()
	if ok, why := v.Available(model.Instance{}); ok || why == "" {
		t.Errorf("no server should refuse and say why: ok=%v why=%q", ok, why)
	}
	if ok, _ := v.Available(model.Instance{Name: "x", Game: "valheim"}); !ok {
		t.Error("a server should be available")
	}
}

func TestEmptyStatesSayWhichEmptyTheyAre(t *testing.T) {
	got := players.New().Render(frame(), snapshot(nil))

	if !strings.Contains(got, "Nobody connected") {
		t.Errorf("a running empty server should say nobody is on:\n%s", got)
	}
	if !strings.Contains(got, "No history yet") {
		t.Errorf("no sessions should say the history is still building:\n%s", got)
	}
}

func TestSessionsAreListed(t *testing.T) {
	snap := snapshot(nil, session("Huldra", 20, 2, 1), session("Bjorn", 21, 1, 2))

	got := players.New().Render(frame(), snap)
	for _, want := range []string{"Huldra", "Bjorn", "2 sessions", "2 people"} {
		if !strings.Contains(got, want) {
			t.Errorf("render is missing %q:\n%s", want, got)
		}
	}
}

// The number the screen exists to produce. Two players on together from 20:00
// to 22:00 across one day is two concurrent players in those hours and none
// elsewhere.
func TestHourlyOccupancyCountsConcurrentPlayers(t *testing.T) {
	sessions := []model.Session{
		session("a", 20, 2, 1),
		session("b", 20, 2, 1),
	}

	hours := players.HourlyOccupancy(sessions, now)

	if got := hours[20]; got < 1.9 || got > 2.1 {
		t.Errorf("occupancy at 20:00 = %.2f, want about 2", got)
	}
	if got := hours[21]; got < 1.9 || got > 2.1 {
		t.Errorf("occupancy at 21:00 = %.2f, want about 2", got)
	}
	if got := hours[3]; got != 0 {
		t.Errorf("occupancy at 03:00 = %.2f, want 0", got)
	}
}

// An open session is counted up to now, not treated as zero-length. A server
// with people on it right now should not read as empty.
func TestHourlyOccupancyCountsOpenSessions(t *testing.T) {
	open := model.Session{
		Server: "valheim-huldra", Player: "Huldra",
		Joined: now.Add(-2 * time.Hour), // 19:07
	}

	hours := players.HourlyOccupancy([]model.Session{open}, now)
	if hours[20] == 0 {
		t.Errorf("an open session contributed nothing at 20:00: %v", hours)
	}
}

// A session that ends before it starts is nonsense a clock change can produce,
// and it must not become a negative or a hang.
func TestHourlyOccupancyIgnoresBackwardsSessions(t *testing.T) {
	backwards := model.Session{
		Joined: now, Left: now.Add(-time.Hour),
	}
	hours := players.HourlyOccupancy([]model.Session{backwards}, now)
	for h, v := range hours {
		if v != 0 {
			t.Errorf("hour %d = %.2f from a backwards session, want 0", h, v)
		}
	}
}

func TestScrollStaysInTheList(t *testing.T) {
	var sessions []model.Session
	for i := 0; i < 40; i++ {
		sessions = append(sessions, session("p", 12, 1, i%7))
	}
	snap := snapshot(nil, sessions...)

	var v tui.View = players.New()
	for i := 0; i < 100; i++ {
		v, _ = v.Update(tea.KeyMsg{Type: tea.KeyDown}, frame(), snap)
	}
	if got := v.Render(frame(), snap); !strings.Contains(got, "SESSIONS") {
		t.Errorf("scrolling past the end broke the render:\n%s", got)
	}

	for i := 0; i < 200; i++ {
		v, _ = v.Update(tea.KeyMsg{Type: tea.KeyUp}, frame(), snap)
	}
	if got := v.Render(frame(), snap); !strings.Contains(got, "SESSIONS") {
		t.Errorf("scrolling past the start broke the render:\n%s", got)
	}
}

func TestNoLineExceedsTheFrame(t *testing.T) {
	snap := snapshot(
		[]model.Player{{Name: "AVeryLongPlayerNameIndeed", SteamID: "76561198000000000", Since: now.Add(-time.Hour)}},
		session("AnotherVeryLongPlayerName", 20, 3, 1),
	)

	for _, width := range []int{70, 92, 120} {
		f := frame()
		f.Width = width
		for i, line := range strings.Split(players.New().Render(f, snap), "\n") {
			if w := comp.Width(line); w > width {
				t.Errorf("width %d: line %d is %d cells", width, i, w)
			}
		}
	}
}

func TestGoldenRenders(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
		snap          core.Snapshot
	}{
		{name: "empty", snap: snapshot(nil)},
		{
			// The narrow layout DESIGN asks every view to have a real one of.
			name: "narrow", width: 80, height: 24,
			snap: snapshot(
				[]model.Player{{Name: "Huldra", SteamID: "76561198000000001", Since: now.Add(-90 * time.Minute)}},
				session("Huldra", 20, 3, 1),
				session("Bjorn", 20, 2, 1),
			),
		},
		{
			name: "busy",
			snap: snapshot(
				[]model.Player{
					{Name: "Huldra", SteamID: "76561198000000001", Since: now.Add(-90 * time.Minute)},
					{Name: "Bjorn", SteamID: "76561198000000002", Since: now.Add(-20 * time.Minute)},
				},
				session("Huldra", 20, 3, 1),
				session("Bjorn", 20, 2, 1),
				session("Huldra", 19, 4, 2),
				session("Sif", 21, 1, 3),
			),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := frame()
			if tt.width > 0 {
				f.Width, f.Height = tt.width, tt.height
			}

			got := players.New().Render(f, tt.snap)
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
