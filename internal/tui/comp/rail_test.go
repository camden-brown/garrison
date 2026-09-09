package comp

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/model"
)

func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii)
	m.Run()
}

var now = time.Date(2026, 9, 9, 21, 7, 0, 0, time.UTC)

func servers() []core.Server {
	return []core.Server{
		{Name: "palworld-sat", State: model.StateCrashed},
		{Name: "valheim-huldra", State: model.StateRunning, Players: []model.Player{{Name: "Dalinar"}}},
		{Name: "zomboid-main", State: model.StateRunning},
	}
}

func rail() Rail {
	return Rail{
		Theme:    NewTheme(false),
		Height:   20,
		Servers:  servers(),
		Selected: "valheim-huldra",
		Entries: []RailEntry{
			{Title: "Dashboard", Key: "1"},
			{Title: "Console", Key: "2"},
			{Title: "Mods", Key: "3", Reason: "Palworld has no mod system"},
		},
		Active: 0,
		Focus:  FocusServers,
	}
}

// The stage is joined to the right of the rail, so a rail line of the wrong
// width makes the stage start in a different column on that row alone — which
// reads as corruption rather than as a layout bug.
func TestEveryLineIsExactlyRailWidth(t *testing.T) {
	cases := map[string]Rail{
		"normal":           rail(),
		"nothing selected": func() Rail { r := rail(); r.Selected = ""; return r }(),
		"no servers":       func() Rail { r := rail(); r.Servers = nil; return r }(),
		"no entries":       func() Rail { r := rail(); r.Entries = nil; return r }(),
		"ascii":            func() Rail { r := rail(); r.Theme = NewTheme(true); return r }(),
		"long names": func() Rail {
			r := rail()
			r.Servers = []core.Server{{Name: "a-server-with-a-truly-unreasonable-name", State: model.StateRunning}}
			return r
		}(),
	}

	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			for i, line := range strings.Split(r.Render(), "\n") {
				if w := lipgloss.Width(line); w != RailWidth {
					t.Errorf("line %d is %d cells, want %d: %q", i, w, RailWidth, line)
				}
			}
		})
	}
}

func TestRenderIsExactlyHeightLines(t *testing.T) {
	for _, h := range []int{5, 20, 60} {
		r := rail()
		r.Height = h
		if got := len(strings.Split(r.Render(), "\n")); got != h {
			t.Errorf("height %d produced %d lines", h, got)
		}
	}
}

func TestServersAndViewsBothAppear(t *testing.T) {
	out := rail().Render()

	for _, want := range []string{"FLEET", "All servers", "palworld-sat", "valheim-huldra", "VIEW", "Dashboard", "SCHEDULE"} {
		if !strings.Contains(out, want) {
			t.Errorf("rail is missing %q:\n%s", want, out)
		}
	}
}

// The view list is about a server, and "Dashboard" on its own does not say
// whose.
func TestViewSectionNamesItsServer(t *testing.T) {
	if out := rail().Render(); !strings.Contains(out, "valheim-huldra") {
		t.Error("the view section does not name the selected server")
	}

	r := rail()
	r.Selected = ""
	if out := r.Render(); !strings.Contains(out, "no server") {
		t.Errorf("with nothing selected the view section should say so:\n%s", out)
	}
}

// A player count belongs to a running server. A stopped one showing 0 reads as
// "nobody is playing" when it means "there is nothing to play on".
func TestPlayerCountOnlyForRunningServers(t *testing.T) {
	// Only the server lines, which start with a state glyph. The VIEW
	// header also carries the selected server's name and is not a row.
	var rows []string
	for _, line := range strings.Split(rail().Render(), "\n") {
		for _, glyph := range []string{"●", "✕", "○", "◐"} {
			if strings.HasPrefix(line, glyph) {
				rows = append(rows, line)
			}
		}
	}
	if len(rows) != 3 {
		t.Fatalf("found %d server rows, want 3", len(rows))
	}

	for _, line := range rows {
		switch {
		case strings.Contains(line, "palworld-sat"), strings.Contains(line, "zomboid-main"):
			if !strings.Contains(line, "—") {
				t.Errorf("a server with no players should show a dash: %q", line)
			}
		case strings.Contains(line, "valheim-huldra"):
			if !strings.HasSuffix(strings.TrimRight(line, " "), "1") {
				t.Errorf("a running server with one player does not show the count: %q", line)
			}
		}
	}
}

// Nothing schedules anything until M2, and an empty heading looks like a
// rendering fault.
func TestScheduleSaysItIsEmpty(t *testing.T) {
	if out := rail().Render(); !strings.Contains(out, "none until M2") {
		t.Errorf("the schedule section is silently empty:\n%s", out)
	}
}
