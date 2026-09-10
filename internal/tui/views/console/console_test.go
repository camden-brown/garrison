package console_test

import (
	"flag"
	"os"
	"path/filepath"
	"strconv"
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
	"github.com/camden-brown/garrison/internal/tui/views/console"

	_ "github.com/camden-brown/garrison/internal/games/all"
)

var update = flag.Bool("update", false, "rewrite the .golden files")

func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii)
	// Timestamps render in the operator's zone, so the goldens need one
	// pinned or they move with whoever runs the suite.
	time.Local = time.UTC
	os.Exit(m.Run())
}

var now = time.Date(2026, 9, 9, 21, 7, 0, 0, time.UTC)

func snapshot(events ...model.Event) core.Snapshot {
	inst := model.Instance{Name: "valheim-huldra", Game: "valheim"}
	s := core.Reduce(core.Snapshot{Engine: core.Engine{OK: true}},
		core.InstancesLoaded{At: now, Instances: []model.Instance{inst}},
		core.FleetObserved{At: now, Containers: []host.Container{
			{Instance: "valheim-huldra", Game: "valheim", State: model.StateRunning},
		}},
	)
	if len(events) == 0 {
		return s
	}
	return core.Reduce(s, core.LogEventsRead{At: now, Server: "valheim-huldra", Events: events})
}

func frame() tui.Frame {
	return tui.Frame{
		Width: 92, Height: 24, Theme: comp.NewTheme(false), Now: now,
		Server: "valheim-huldra", Focused: true,
	}
}

func ev(kind model.Kind, text string) model.Event {
	return model.Event{Kind: kind, At: now, Text: text}
}

func press(v tui.View, snap core.Snapshot, keys ...string) tui.View {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "space":
			msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "pgup":
			msg = tea.KeyMsg{Type: tea.KeyPgUp}
		case "pgdown":
			msg = tea.KeyMsg{Type: tea.KeyPgDown}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		v, _ = v.Update(msg, frame(), snap)
	}
	return v
}

func TestNeedsAServer(t *testing.T) {
	v := console.New()
	if ok, why := v.Available(model.Instance{}); ok || why == "" {
		t.Errorf("a console with no server should refuse and say why: ok=%v why=%q", ok, why)
	}
	if ok, _ := v.Available(model.Instance{Name: "valheim-huldra", Game: "valheim"}); !ok {
		t.Error("a console with a server should be available")
	}
}

// The whole reason CollapseRepeats exists: a crash loop must not erase what
// caused it.
func TestRepeatedLinesCollapse(t *testing.T) {
	events := []model.Event{ev(model.KindError, "mod exploded")}
	for i := 0; i < 40; i++ {
		events = append(events, ev(model.KindWarn, "retrying"))
	}
	snap := snapshot(events...)

	got := console.New().Render(frame(), snap)
	if !strings.Contains(got, "40×") {
		t.Errorf("render does not collapse the flood into a counter:\n%s", got)
	}
	if !strings.Contains(got, "mod exploded") {
		t.Errorf("the flood pushed the cause off screen:\n%s", got)
	}
}

func TestFilterNarrowsToOneClass(t *testing.T) {
	snap := snapshot(
		ev(model.KindChat, "hello there"),
		ev(model.KindError, "a problem"),
		ev(model.KindInfo, "some noise"),
	)

	// f cycles all → chat.
	v := press(console.New(), snap, "f")
	got := v.Render(frame(), snap)

	if !strings.Contains(got, "hello there") {
		t.Errorf("chat filter dropped the chat line:\n%s", got)
	}
	if strings.Contains(got, "a problem") || strings.Contains(got, "some noise") {
		t.Errorf("chat filter kept a non-chat line:\n%s", got)
	}
	if !strings.Contains(got, "filter: chat") {
		t.Errorf("the filter is not named in the panel title:\n%s", got)
	}
}

func TestFilterCyclesBackToAll(t *testing.T) {
	snap := snapshot(ev(model.KindInfo, "some noise"))

	v := press(console.New(), snap, "f", "f", "f", "f")
	if got := v.Render(frame(), snap); !strings.Contains(got, "some noise") {
		t.Errorf("four presses should return to all:\n%s", got)
	}
}

// Scrolling back and then following again is the whole interaction. Getting
// the clamp wrong shows blank rows above real ones, or refuses to move.
func TestScrollbackAndReturn(t *testing.T) {
	var events []model.Event
	for i := 0; i < 200; i++ {
		events = append(events, ev(model.KindInfo, "line "+strconv.Itoa(i)))
	}
	snap := snapshot(events...)

	newest := console.New().Render(frame(), snap)
	if !strings.Contains(newest, "line 199") {
		t.Errorf("a fresh console does not show the newest line:\n%s", newest)
	}

	back := press(console.New(), snap, "pgup", "pgup").Render(frame(), snap)
	if strings.Contains(back, "line 199") {
		t.Errorf("paging back still shows the newest line:\n%s", back)
	}
	if !strings.Contains(back, "back") {
		t.Errorf("paging back is not reported in the title:\n%s", back)
	}

	returned := press(console.New(), snap, "pgup", "pgup", "G").Render(frame(), snap)
	if !strings.Contains(returned, "line 199") {
		t.Errorf("G did not return to the newest line:\n%s", returned)
	}
}

func TestScrollCannotRunOffEitherEnd(t *testing.T) {
	snap := snapshot(ev(model.KindInfo, "only line"))

	for _, keys := range [][]string{{"up", "up", "up"}, {"down", "down"}, {"g"}, {"G"}} {
		got := press(console.New(), snap, keys...).Render(frame(), snap)
		if !strings.Contains(got, "only line") {
			t.Errorf("%v scrolled away from the only line:\n%s", keys, got)
		}
	}
}

// Freezing has to hold the window as new output arrives, or it is a label
// rather than a behaviour.
func TestFreezeHoldsThePosition(t *testing.T) {
	var events []model.Event
	for i := 0; i < 30; i++ {
		events = append(events, ev(model.KindInfo, "old "+strconv.Itoa(i)))
	}
	snap := snapshot(events...)

	v := press(console.New(), snap, "space")
	if got := v.Render(frame(), snap); !strings.Contains(got, "FROZEN") {
		t.Errorf("freezing is not reported:\n%s", got)
	}

	// More output arrives while frozen.
	var more []model.Event
	for i := 0; i < 30; i++ {
		more = append(more, ev(model.KindInfo, "new "+strconv.Itoa(i)))
	}
	after := core.Reduce(snap, core.LogEventsRead{At: now, Server: "valheim-huldra", Events: more})

	got := v.Render(frame(), after)
	if strings.Contains(got, "new 29") {
		t.Errorf("a frozen console followed the new output:\n%s", got)
	}
	if !strings.Contains(got, "old 29") {
		t.Errorf("a frozen console lost the line it was parked on:\n%s", got)
	}

	// Unfreezing catches up.
	caught := press(v, after, "space").Render(frame(), after)
	if !strings.Contains(caught, "new 29") {
		t.Errorf("unfreezing did not catch up:\n%s", caught)
	}
}

// Valheim has no RCON, and the view must say so rather than offer an input
// that drops what is typed into it. The assertion is games.Commandable, so
// this passes without the view knowing which game it is looking at.
func TestCommandLineExplainsItselfWithoutACommandChannel(t *testing.T) {
	snap := snapshot(ev(model.KindInfo, "up"))

	got := console.New().Render(frame(), snap)
	if !strings.Contains(got, "no command channel") {
		t.Errorf("the console does not explain the missing command channel:\n%s", got)
	}
}

func TestEmptyConsoleSaysSo(t *testing.T) {
	got := console.New().Render(frame(), snapshot())
	if !strings.Contains(got, "Nothing yet") {
		t.Errorf("an empty console should say so:\n%s", got)
	}
}

func TestNoLineExceedsTheFrame(t *testing.T) {
	snap := snapshot(
		ev(model.KindError, strings.Repeat("a very long error message ", 20)),
		ev(model.KindChat, "短い行"),
	)

	for _, width := range []int{70, 92, 120} {
		f := frame()
		f.Width = width
		for i, line := range strings.Split(console.New().Render(f, snap), "\n") {
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
		setup         func(tui.View, core.Snapshot) tui.View
	}{
		{
			name: "clean",
			snap: snapshot(
				ev(model.KindInfo, "server started"),
				ev(model.KindJoin, "Huldra joined"),
				ev(model.KindChat, "<Huldra> anyone seen the boat"),
				ev(model.KindSave, "world saved in 314ms"),
				ev(model.KindWarn, "retrying"),
				ev(model.KindWarn, "retrying"),
				ev(model.KindWarn, "retrying"),
				ev(model.KindLeave, "Huldra left"),
			),
		},
		{
			// The narrow layout DESIGN asks every view to have a real one of.
			name: "narrow", width: 80, height: 24,
			snap: snapshot(
				ev(model.KindJoin, "Huldra joined"),
				ev(model.KindChat, "<Huldra> anyone seen the boat"),
				ev(model.KindError, "a rather long error message that will not fit in eighty columns"),
			),
		},
		{
			name: "filtered",
			snap: snapshot(
				ev(model.KindChat, "<Huldra> anyone seen the boat"),
				ev(model.KindError, "mod exploded"),
				ev(model.KindInfo, "server started"),
			),
			setup: func(v tui.View, snap core.Snapshot) tui.View {
				return press(v, snap, "f", "f", "f")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v tui.View = console.New()
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

// "/" narrows by content, where "f" narrows by classification. Both are the
// view's own, and they compose.
func TestTextFilterNarrowsByContent(t *testing.T) {
	snap := snapshot(
		ev(model.KindInfo, "world saved in 314ms"),
		ev(model.KindInfo, "player connected"),
	)

	v := press(console.New(), snap, "/")
	v = press(v, snap, "s", "a", "v", "e")

	got := v.Render(frame(), snap)
	if !strings.Contains(got, "world saved") {
		t.Errorf("the matching line is gone:\n%s", got)
	}
	if strings.Contains(got, "player connected") {
		t.Errorf("a non-matching line survived:\n%s", got)
	}
}

// While the bar is open every key is text, or typing "freeze" freezes the
// console and cycles the class filter on the way past.
func TestTextFilterSwallowsTheViewsKeys(t *testing.T) {
	snap := snapshot(ev(model.KindInfo, "free"))

	v := press(console.New(), snap, "/")
	v = press(v, snap, "f", "r", "e", "e")

	got := v.Render(frame(), snap)
	if strings.Contains(got, "FROZEN") {
		t.Errorf("typing into the filter froze the console:\n%s", got)
	}
	if strings.Contains(got, "filter: chat") {
		t.Errorf("typing into the filter cycled the class filter:\n%s", got)
	}
	if !strings.Contains(got, "free") {
		t.Errorf("the matching line is gone:\n%s", got)
	}
}

func TestTextFilterEscapeRestoresEverything(t *testing.T) {
	snap := snapshot(
		ev(model.KindInfo, "world saved"),
		ev(model.KindInfo, "player connected"),
	)

	v := press(console.New(), snap, "/")
	v = press(v, snap, "s", "a", "v", "e")
	v = press(v, snap, "esc")

	if got := v.Render(frame(), snap); !strings.Contains(got, "player connected") {
		t.Errorf("esc did not restore the unfiltered log:\n%s", got)
	}
}

// Nothing matching says so, rather than showing an empty panel that looks
// like a server producing no output.
func TestTextFilterWithNoMatchesExplainsItself(t *testing.T) {
	snap := snapshot(ev(model.KindInfo, "world saved"))

	v := press(console.New(), snap, "/")
	v = press(v, snap, "z", "z", "z")

	if got := v.Render(frame(), snap); !strings.Contains(got, "Nothing matching") {
		t.Errorf("an empty filter result does not explain itself:\n%s", got)
	}
}
