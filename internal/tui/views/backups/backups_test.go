package backups_test

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
	"github.com/camden-brown/garrison/internal/tui/views/backups"

	_ "github.com/camden-brown/garrison/internal/games/all"
)

var update = flag.Bool("update", false, "rewrite the .golden files")

func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii)
	os.Exit(m.Run())
}

var now = time.Date(2026, 9, 9, 21, 7, 0, 0, time.UTC)

func archive(name string, hoursAgo int, bytes int64) model.Archive {
	return model.Archive{
		Path:  "/data/backups/" + name,
		Name:  name,
		Taken: now.Add(-time.Duration(hoursAgo) * time.Hour),
		Bytes: bytes,
	}
}

// snapshot builds a fleet with one server. listed says whether the archive
// poller has answered, which is what separates "none" from "not looked yet".
func snapshot(listed bool, archives ...model.Archive) core.Snapshot {
	inst := model.Instance{Name: "valheim-huldra", Game: "valheim", Data: `C:\gameservers\huldra`}
	s := core.Reduce(core.Snapshot{Engine: core.Engine{OK: true}},
		core.InstancesLoaded{At: now, Instances: []model.Instance{inst}},
		core.FleetObserved{At: now, Containers: []host.Container{
			{Instance: "valheim-huldra", Game: "valheim", State: model.StateRunning},
		}},
	)
	if !listed {
		return s
	}
	return core.Reduce(s, core.BackupsListed{At: now, Server: "valheim-huldra", Archive: archives})
}

func frame() tui.Frame {
	return tui.Frame{
		Width: 92, Height: 24, Theme: comp.NewTheme(false), Now: now,
		Server: "valheim-huldra", Focused: true,
	}
}

func press(v tui.View, snap core.Snapshot, keys ...string) (tui.View, []tea.Msg) {
	var msgs []tea.Msg
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		next, cmd := v.Update(msg, frame(), snap)
		v = next
		if cmd != nil {
			msgs = append(msgs, cmd())
		}
	}
	return v, msgs
}

// typeName presses one key per rune, which is what typing a server name into
// the confirmation actually is.
func typeName(v tui.View, snap core.Snapshot, name string) (tui.View, []tea.Msg) {
	keys := make([]string, 0, len(name))
	for _, r := range name {
		keys = append(keys, string(r))
	}
	return press(v, snap, keys...)
}

func TestNeedsAServerWithADataDirectory(t *testing.T) {
	v := backups.New()

	if ok, why := v.Available(model.Instance{}); ok || why == "" {
		t.Errorf("no server should refuse and say why: ok=%v why=%q", ok, why)
	}
	if ok, why := v.Available(model.Instance{Name: "x"}); ok || !strings.Contains(why, "data directory") {
		t.Errorf("a server with no data directory should refuse: ok=%v why=%q", ok, why)
	}
	if ok, _ := v.Available(model.Instance{Name: "x", Data: "/data"}); !ok {
		t.Error("a configured server should be available")
	}
}

// "None" and "not looked yet" are different answers, and only one of them is
// a reason to worry.
func TestEmptyIsNotTheSameAsUnknown(t *testing.T) {
	unknown := backups.New().Render(frame(), snapshot(false))
	if !strings.Contains(unknown, "Looking") {
		t.Errorf("an unpolled server should not claim it has no backups:\n%s", unknown)
	}

	none := backups.New().Render(frame(), snapshot(true))
	if !strings.Contains(none, "No backups yet") {
		t.Errorf("a polled empty server should say so:\n%s", none)
	}
}

func TestArchivesAreListedNewestFirst(t *testing.T) {
	snap := snapshot(true,
		archive("2026-09-07T0300.tar.zst", 50, 1<<20),
		archive("2026-09-09T0300.tar.zst", 2, 3<<20),
		archive("2026-09-08T0300.tar.zst", 26, 2<<20),
	)

	got := backups.New().Render(frame(), snap)
	newest := strings.Index(got, "2026-09-09T0300")
	middle := strings.Index(got, "2026-09-08T0300")
	oldest := strings.Index(got, "2026-09-07T0300")

	if newest < 0 || middle < 0 || oldest < 0 {
		t.Fatalf("not every archive is listed:\n%s", got)
	}
	if !(newest < middle && middle < oldest) {
		t.Errorf("archives are not newest first:\n%s", got)
	}
}

func TestBackupNowSubmits(t *testing.T) {
	snap := snapshot(true)

	_, msgs := press(backups.New(), snap, "b")
	if len(msgs) != 1 {
		t.Fatalf("b produced %d messages, want 1", len(msgs))
	}
	action, ok := msgs[0].(tui.ActionMsg)
	if !ok || action.Op != core.OpBackup || action.Server != "valheim-huldra" {
		t.Errorf("b produced %#v, want a backup for the server", msgs[0])
	}
}

// The prompt is the point: a restore does not go anywhere until the server's
// name has been typed out in full.
func TestRestoreRequiresTheServerNameTyped(t *testing.T) {
	snap := snapshot(true, archive("2026-09-09T0300.tar.zst", 2, 3<<20))

	v, msgs := press(backups.New(), snap, "B")
	if len(msgs) != 0 {
		t.Fatalf("B restored immediately: %#v", msgs)
	}
	if got := v.Render(frame(), snap); !strings.Contains(got, "Type the server name") {
		t.Errorf("no confirmation prompt:\n%s", got)
	}

	// Enter with nothing typed does nothing.
	v, msgs = press(v, snap, "enter")
	if len(msgs) != 0 {
		t.Fatalf("an empty confirmation restored: %#v", msgs)
	}

	// The wrong name does nothing either.
	v, _ = typeName(v, snap, "valheim")
	v, msgs = press(v, snap, "enter")
	if len(msgs) != 0 {
		t.Fatalf("a partial name restored: %#v", msgs)
	}

	// The full name goes through.
	v, _ = typeName(v, snap, "valheim-huldra")
	_, msgs = press(v, snap, "enter")
	if len(msgs) != 1 {
		t.Fatalf("the correct name produced %d messages, want 1", len(msgs))
	}
	action, ok := msgs[0].(tui.ActionMsg)
	if !ok || action.Op != core.OpRestore {
		t.Fatalf("got %#v, want a restore", msgs[0])
	}
	if action.Archive != "/data/backups/2026-09-09T0300.tar.zst" {
		t.Errorf("restore names %q, want the selected archive", action.Archive)
	}
}

// A near miss clears rather than leaving the wrong name to be corrected into
// the right one by a stray keystroke.
func TestAWrongNameClearsTheField(t *testing.T) {
	snap := snapshot(true, archive("2026-09-09T0300.tar.zst", 2, 3<<20))

	v, _ := press(backups.New(), snap, "B")
	v, _ = typeName(v, snap, "valheim-huldrx")
	v, _ = press(v, snap, "enter")

	if got := v.Render(frame(), snap); strings.Contains(got, "valheim-huldrx") {
		t.Errorf("the wrong name was left in the field:\n%s", got)
	}
}

func TestEscapeCancelsTheRestore(t *testing.T) {
	snap := snapshot(true, archive("2026-09-09T0300.tar.zst", 2, 3<<20))

	v, _ := press(backups.New(), snap, "B")
	v, _ = typeName(v, snap, "valheim-huldra")
	v, msgs := press(v, snap, "esc")

	if len(msgs) != 0 {
		t.Fatalf("esc restored anyway: %#v", msgs)
	}
	if got := v.Render(frame(), snap); strings.Contains(got, "Type the server name") {
		t.Errorf("esc left the prompt up:\n%s", got)
	}
}

// The confirmation has to swallow the view's own keys, or typing a name with a
// b in it takes a backup halfway through.
func TestConfirmationSwallowsTheOtherKeys(t *testing.T) {
	snap := snapshot(true, archive("2026-09-09T0300.tar.zst", 2, 3<<20))

	v, _ := press(backups.New(), snap, "B")
	_, msgs := press(v, snap, "b")

	for _, msg := range msgs {
		if action, ok := msg.(tui.ActionMsg); ok && action.Op == core.OpBackup {
			t.Error("typing b during the confirmation took a backup")
		}
	}
}

func TestRestoreWithNothingToRestoreDoesNotPrompt(t *testing.T) {
	snap := snapshot(true)

	v, _ := press(backups.New(), snap, "B")
	if got := v.Render(frame(), snap); strings.Contains(got, "Type the server name") {
		t.Errorf("prompted to restore with no archives:\n%s", got)
	}
}

func TestCursorCannotLeaveTheList(t *testing.T) {
	snap := snapshot(true,
		archive("2026-09-09T0300.tar.zst", 2, 1<<20),
		archive("2026-09-08T0300.tar.zst", 26, 1<<20),
	)

	v, _ := press(backups.New(), snap, "up", "up", "up")
	if got := v.Render(frame(), snap); !strings.Contains(got, "2026-09-09T0300") {
		t.Errorf("cursor left the top of the list:\n%s", got)
	}

	v, _ = press(backups.New(), snap, "down", "down", "down")
	v, msgs := press(v, snap, "B")
	v, _ = typeName(v, snap, "valheim-huldra")
	_, msgs = press(v, snap, "enter")

	if len(msgs) != 1 {
		t.Fatalf("got %d messages", len(msgs))
	}
	if action := msgs[0].(tui.ActionMsg); !strings.Contains(action.Archive, "2026-09-08") {
		t.Errorf("cursor ran past the end: selected %q", action.Archive)
	}
}

func TestNoLineExceedsTheFrame(t *testing.T) {
	snap := snapshot(true,
		archive("2026-09-09T0300.tar.zst", 2, 3<<30),
		archive("2026-09-08T0300.tar.zst", 26, 1<<20),
	)

	for _, width := range []int{70, 92, 120} {
		f := frame()
		f.Width = width
		v, _ := press(backups.New(), snap, "B")
		for i, line := range strings.Split(v.Render(f, snap), "\n") {
			if w := comp.Width(line); w > width {
				t.Errorf("width %d: line %d is %d cells", width, i, w)
			}
		}
	}
}

func TestGoldenRenders(t *testing.T) {
	list := snapshot(true,
		archive("2026-09-09T0300.tar.zst", 2, 3<<20),
		archive("2026-09-08T0300.tar.zst", 26, 2<<20),
		archive("2026-09-07T0300.tar.zst", 50, 1<<20),
	)

	tests := []struct {
		name          string
		width, height int
		snap          core.Snapshot
		setup         func(tui.View, core.Snapshot) tui.View
	}{
		{name: "clean", snap: list},
		{name: "narrow", width: 80, height: 24, snap: list},
		{name: "empty", snap: snapshot(true)},
		{
			name: "confirming",
			snap: list,
			setup: func(v tui.View, snap core.Snapshot) tui.View {
				out, _ := press(v, snap, "B")
				out, _ = typeName(out, snap, "valheim-hul")
				return out
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v tui.View = backups.New()
			if tt.setup != nil {
				v = tt.setup(v, tt.snap)
			}

			f := frame()
			if tt.width > 0 {
				f.Width, f.Height = tt.width, tt.height
			}

			got := v.Render(f, tt.snap)
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
