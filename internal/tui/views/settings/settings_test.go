package settings_test

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/games"
	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tui"
	"github.com/camden-brown/garrison/internal/tui/comp"
	"github.com/camden-brown/garrison/internal/tui/views/settings"

	_ "github.com/camden-brown/garrison/internal/games/all"
)

var update = flag.Bool("update", false, "rewrite the .golden files")

func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii)
	os.Exit(m.Run())
}

var now = time.Date(2026, 9, 9, 21, 7, 0, 0, time.UTC)

func snapshot(settings map[string]any) core.Snapshot {
	inst := model.Instance{Name: "valheim-huldra", Game: "valheim", Settings: settings}
	return core.Reduce(core.Snapshot{Engine: core.Engine{OK: true}},
		core.InstancesLoaded{At: now, Instances: []model.Instance{inst}},
		core.FleetObserved{At: now, Containers: []host.Container{
			{Instance: "valheim-huldra", Game: "valheim", State: model.StateRunning},
		}},
	)
}

func frame() tui.Frame {
	return tui.Frame{
		Width: 92, Height: 30, Theme: comp.NewTheme(false), Now: now,
		Server: "valheim-huldra", Focused: true,
	}
}

func press(v tui.View, snap core.Snapshot, keys ...string) (tui.View, []tea.Msg) {
	var msgs []tea.Msg
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "space":
			msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		case "right":
			msg = tea.KeyMsg{Type: tea.KeyRight}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "backspace":
			msg = tea.KeyMsg{Type: tea.KeyBackspace}
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

// pressIn is press against a given frame. Update resolves the server from the
// frame, so a test driving a server other than the default needs this.
func pressIn(v tui.View, f tui.Frame, snap core.Snapshot, keys ...string) (tui.View, []tea.Msg) {
	var msgs []tea.Msg
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "space":
			msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
		case "right":
			msg = tea.KeyMsg{Type: tea.KeyRight}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		next, cmd := v.Update(msg, f, snap)
		v = next
		if cmd != nil {
			msgs = append(msgs, cmd())
		}
	}
	return v, msgs
}

// typeKeys presses keys and feeds every edit the view emits back into the
// snapshot, which is what the shell does. Without it the draft never moves and
// each keystroke would be applied to the same starting value.
func typeKeys(v tui.View, snap core.Snapshot, keys ...string) (tui.View, core.Snapshot) {
	for _, k := range keys {
		next, msgs := press(v, snap, k)
		v = next
		for _, msg := range msgs {
			if edit, ok := msg.(tui.EditMsg); ok {
				snap = core.Reduce(snap, core.SettingEdited{
					At: now, Server: edit.Server, Key: edit.Key, Value: edit.Value,
				})
			}
		}
	}
	return v, snap
}

// Typing goes straight into the store's draft. There is no buffer in the view
// to get out of step with it, which is the point of the split.
func TestTypingEditsTheDraft(t *testing.T) {
	snap := snapshot(map[string]any{"ServerName": "Huldra"})

	// right into the fields, down to World name, enter to edit, then type.
	_, snap = typeKeys(settings.New(), snap, "right", "down", "enter", "M", "i", "d")

	srv, ok := snap.Server("valheim-huldra")
	if !ok {
		t.Fatal("no server in the snapshot")
	}
	if got, _ := srv.Setting("WorldName"); got != "Mid" {
		t.Errorf("WorldName draft = %v, want %q", got, "Mid")
	}
}

func TestBackspaceEditsTheDraft(t *testing.T) {
	snap := snapshot(map[string]any{"ServerName": "Huldra"})
	_, snap = typeKeys(settings.New(), snap, "right", "enter", "backspace", "backspace")

	srv, _ := snap.Server("valheim-huldra")
	if got, _ := srv.Setting("ServerName"); got != "Huld" {
		t.Errorf("ServerName draft = %v, want %q", got, "Huld")
	}
}

// While a field has the keyboard, form keys are text. Otherwise naming a
// server "Advanced" would toggle the advanced fields four times and discard
// the form on the D.
func TestEditingSwallowsFormKeys(t *testing.T) {
	snap := snapshot(map[string]any{"ServerName": "Huldra"})

	v, _ := press(settings.New(), snap, "right", "enter")
	_, msgs := press(v, snap, "D")

	for _, msg := range msgs {
		if _, ok := msg.(tui.DiscardMsg); ok {
			t.Error("D discarded the form while typing, want it typed")
		}
	}
}

func TestEscapeEndsEditing(t *testing.T) {
	snap := snapshot(map[string]any{"ServerName": "Huldra"})

	v, _ := press(settings.New(), snap, "right", "enter", "esc")
	_, msgs := press(v, snap, "D")

	var discarded bool
	for _, msg := range msgs {
		if _, ok := msg.(tui.DiscardMsg); ok {
			discarded = true
		}
	}
	if !discarded {
		t.Error("D was still swallowed after esc, want editing ended")
	}
}

// A password has nowhere to go but the TOML file, so the form does not offer
// to put one there. See ADR 0009.
func TestSecretsAreNotEditableInTheForm(t *testing.T) {
	snap := snapshot(map[string]any{"ServerName": "Huldra"})

	// right into the fields, left/right to Access, down to the password.
	v, _ := press(settings.New(), snap, "down", "right", "down", "enter")
	_, msgs := press(v, snap, "x")

	for _, msg := range msgs {
		if edit, ok := msg.(tui.EditMsg); ok && edit.Key == "ServerPass" {
			t.Errorf("typing edited the password to %v, want it left alone", edit.Value)
		}
	}
}

// The whole trade: the form is generated from the plugin's schema, so every
// field Valheim declares appears without a line of Valheim-specific code here.
func TestEveryDeclaredFieldIsRendered(t *testing.T) {
	g, err := games.Get("valheim")
	if err != nil {
		t.Fatalf("valheim is not registered: %v", err)
	}

	var v tui.View = settings.New()
	snap := snapshot(map[string]any{})

	// Visit every group, collecting what is shown.
	shown := map[string]bool{}
	for i := 0; i < len(g.Schema().Groups()); i++ {
		out := v.Render(frame(), snap)
		for _, f := range g.Schema().Fields {
			if strings.Contains(out, f.Label) {
				shown[f.Key] = true
			}
		}
		v, _ = press(v, snap, "down")
	}

	for _, f := range g.Schema().Fields {
		if f.Advanced {
			continue
		}
		if !shown[f.Key] {
			t.Errorf("field %q (%s) never appeared in the form", f.Key, f.Label)
		}
	}
}

// A change is a message to the shell, not a write. The draft lives in the
// store because applying it is a task.
func TestTogglingABoolEmitsAnEdit(t *testing.T) {
	snap := snapshot(map[string]any{"ServerPublic": false})

	var v tui.View = settings.New()
	// Into the fields pane, onto the Access group where the bool lives.
	v, _ = press(v, snap, "down", "right")

	_, msgs := press(v, snap, "down", "space")

	var edit tui.EditMsg
	var found bool
	for _, m := range msgs {
		if e, ok := m.(tui.EditMsg); ok {
			edit, found = e, true
		}
	}
	if !found {
		t.Fatal("toggling produced no edit")
	}
	if edit.Server != "valheim-huldra" {
		t.Errorf("edit is for %q", edit.Server)
	}
}

// A secret is never rendered. Showing its length would leak more than nothing.
func TestSecretsAreNeverShown(t *testing.T) {
	snap := snapshot(map[string]any{"ServerPass": "hunter2-and-then-some"})

	var v tui.View = settings.New()
	for i := 0; i < 4; i++ {
		if out := v.Render(frame(), snap); strings.Contains(out, "hunter2") {
			t.Fatalf("the password is on screen:\n%s", out)
		}
		v, _ = press(v, snap, "down")
	}
}

// The form's job is to say what applying costs before anything is written.
func TestFooterNamesTheWorstImpact(t *testing.T) {
	snap := snapshot(map[string]any{"WorldName": "Huldra"})
	// A world-name change can start a new world, which is the wipe risk.
	snap = core.Reduce(snap, core.SettingEdited{
		At: now, Server: "valheim-huldra", Key: "WorldName", Value: "Somewhere Else",
	})

	out := settings.New().Render(frame(), snap)
	if !strings.Contains(out, "1 unapplied change") {
		t.Errorf("the footer does not count the change:\n%s", out)
	}
	if !strings.Contains(strings.ToUpper(out), "RISK") {
		t.Errorf("the footer does not warn about the wipe risk:\n%s", out)
	}
}

func TestCleanFormSaysSo(t *testing.T) {
	out := settings.New().Render(frame(), snapshot(map[string]any{}))
	if !strings.Contains(out, "No unapplied changes") {
		t.Errorf("a clean form does not say so:\n%s", out)
	}
}

// A game with no plugin, or no settings, explains itself rather than showing
// an empty form — the view answers this, so no game knowledge reaches the shell.
func TestUnavailableExplainsItself(t *testing.T) {
	v := settings.New()

	if ok, why := v.Available(model.Instance{}); ok || why == "" {
		t.Error("with no server selected the view should explain itself")
	}
	if ok, why := v.Available(model.Instance{Name: "x", Game: "nosuchgame"}); ok || !strings.Contains(why, "nosuchgame") {
		t.Errorf("an unknown game should be named: ok=%v why=%q", ok, why)
	}
	if ok, _ := v.Available(model.Instance{Name: "x", Game: "valheim"}); !ok {
		t.Error("valheim has settings and should be available")
	}
}

func TestGoldenRenders(t *testing.T) {
	tests := []struct {
		name  string
		snap  core.Snapshot
		setup func(tui.View, core.Snapshot) tui.View
	}{
		{name: "clean", snap: snapshot(map[string]any{"ServerName": "Huldra", "WorldName": "Huldra"})},
		{
			name: "edited",
			snap: core.Reduce(
				snapshot(map[string]any{"ServerName": "Huldra", "WorldName": "Huldra"}),
				core.SettingEdited{At: now, Server: "valheim-huldra", Key: "ServerName", Value: "Second Huldra"},
			),
		},
		{
			name: "fields-focused",
			snap: snapshot(map[string]any{"ServerName": "Huldra"}),
			setup: func(v tui.View, snap core.Snapshot) tui.View {
				out, _ := press(v, snap, "right", "down")
				return out
			},
		},
		{
			// A text field with the keyboard, caret past the last rune.
			name: "editing",
			snap: snapshot(map[string]any{"ServerName": "Huldra"}),
			setup: func(v tui.View, snap core.Snapshot) tui.View {
				out, _ := press(v, snap, "right", "enter")
				return out
			},
		},
		{
			// The enum group. Every field here is a closed list the plugin
			// declared, so this is where a game growing a new setting shows
			// up as a render change rather than as nothing at all.
			name: "world-modifiers",
			snap: snapshot(map[string]any{"ServerName": "Huldra", "DeathPenalty": "casual"}),
			setup: func(v tui.View, snap core.Snapshot) tui.View {
				out, _ := press(v, snap, "down", "down", "right")
				return out
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var v tui.View = settings.New()
			if tt.setup != nil {
				v = tt.setup(v, tt.snap)
			}

			got := v.Render(frame(), tt.snap)
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
	snap := snapshot(map[string]any{"ServerName": "Huldra"})
	for _, width := range []int{70, 92, 120} {
		f := frame()
		f.Width = width
		for i, line := range strings.Split(settings.New().Render(f, snap), "\n") {
			if w := comp.Width(line); w > width {
				t.Errorf("width %d: line %d is %d cells", width, i, w)
			}
		}
	}
}

// filesGame writes config files, which Valheim does not. Without it the apply
// diff could only ever be tested in the form it takes for a game that keeps
// its settings in the environment.
type filesGame struct{}

func (filesGame) Meta() games.Meta {
	return games.Meta{ID: "filesgame", Name: "Filesgame", DefaultImage: "example/filesgame"}
}
func (filesGame) Plan(model.Instance) (model.Plan, error) { return model.Plan{}, nil }
func (filesGame) Parse(string) model.Event                { return model.Event{} }
func (filesGame) Schema() games.Schema {
	return games.Schema{Fields: []games.Field{
		{Key: "MaxPlayers", Label: "Max players", Group: "Gameplay", Type: games.TypeInt,
			Default: 8, Min: 1, Max: 64, Impact: games.ImpactRestart},
		{Key: "WorldName", Label: "World", Group: "Gameplay", Type: games.TypeString,
			Default: "", Impact: games.ImpactWipeRisk},
	}}
}

// Compile writes a key=value file, with a fixed preamble so the diff has
// unchanged context around the change.
func (filesGame) Compile(inst model.Instance) ([]model.File, error) {
	var b strings.Builder
	b.WriteString("# generated by garrison\n")
	b.WriteString("[server]\n")
	fmt.Fprintf(&b, "MaxPlayers=%v\n", settingOr(inst, "MaxPlayers", 8))
	fmt.Fprintf(&b, "World=%v\n", settingOr(inst, "WorldName", "default"))
	b.WriteString("Difficulty=normal\n")
	return []model.File{{Path: "server.ini", Data: []byte(b.String())}}, nil
}

func settingOr(inst model.Instance, key string, fallback any) any {
	if v, ok := inst.Settings[key]; ok && v != nil && v != "" {
		return v
	}
	return fallback
}

func init() { games.Register(filesGame{}) }

func filesSnapshot(settings map[string]any, ms ...core.Mutation) core.Snapshot {
	inst := model.Instance{Name: "files-one", Game: "filesgame", Data: "/data", Settings: settings}
	base := core.Reduce(core.Snapshot{Engine: core.Engine{OK: true}},
		core.InstancesLoaded{At: now, Instances: []model.Instance{inst}},
		core.FleetObserved{At: now, Containers: []host.Container{
			{Instance: "files-one", Game: "filesgame", State: model.StateRunning},
		}},
	)
	return core.Reduce(base, ms...)
}

func filesFrame() tui.Frame {
	f := frame()
	f.Server = "files-one"
	return f
}

// The point of the diff: the form is an abstraction over files, and impact
// badges say what a change costs without saying what it writes.
func TestApplyShowsTheGeneratedDiff(t *testing.T) {
	snap := filesSnapshot(map[string]any{"MaxPlayers": 8, "WorldName": "Midgard"},
		core.SettingEdited{At: now, Server: "files-one", Key: "MaxPlayers", Value: 16})

	v, _ := pressIn(settings.New(), filesFrame(), snap, "A") // apply -> confirm
	got := v.Render(filesFrame(), snap)

	if !strings.Contains(got, "server.ini") {
		t.Errorf("the diff does not name the file:\n%s", got)
	}
	if !strings.Contains(got, "- MaxPlayers=8") || !strings.Contains(got, "+ MaxPlayers=16") {
		t.Errorf("the diff does not show the change:\n%s", got)
	}
	// A line that did not move must not be reported as changed.
	if strings.Contains(got, "- Difficulty") || strings.Contains(got, "+ Difficulty") {
		t.Errorf("an unchanged line is reported as changed:\n%s", got)
	}
}

// A game that keeps settings in the environment has no file to diff, and that
// is a different answer from "nothing changed".
func TestApplyExplainsAGameThatWritesNoFiles(t *testing.T) {
	snap := core.Reduce(snapshot(map[string]any{"ServerName": "Huldra"}),
		core.SettingEdited{At: now, Server: "valheim-huldra", Key: "ServerName", Value: "Other"})

	v, _ := press(settings.New(), snap, "A")
	got := v.Render(frame(), snap)

	if !strings.Contains(got, "no config files") {
		t.Errorf("a game with no config files should say so:\n%s", got)
	}
}

// One of the three places DESIGN puts deliberate friction: a WipeRisk change
// is confirmed by typing the server's name, not by a keystroke a reflex can
// supply.
func TestAWipeRiskChangeNeedsTheNameTyped(t *testing.T) {
	snap := filesSnapshot(map[string]any{"MaxPlayers": 8, "WorldName": "Midgard"},
		core.SettingEdited{At: now, Server: "files-one", Key: "WorldName", Value: "Elsewhere"})

	v, _ := pressIn(settings.New(), filesFrame(), snap, "A")
	if got := v.Render(filesFrame(), snap); !strings.Contains(got, "Type the server name") {
		t.Fatalf("a wipe risk was not asked to be typed out:\n%s", got)
	}

	// y is not enough any more.
	v2, msgs := pressIn(v, filesFrame(), snap, "y")
	for _, msg := range msgs {
		if _, ok := msg.(tui.ActionMsg); ok {
			t.Fatal("y applied a wipe-risk change")
		}
	}

	// The wrong name is not enough either.
	v3, _ := pressRunesIn(v2, filesFrame(), snap, "files")
	_, msgs = pressIn(v3, filesFrame(), snap, "enter")
	for _, msg := range msgs {
		if _, ok := msg.(tui.ActionMsg); ok {
			t.Fatal("a partial name applied a wipe-risk change")
		}
	}

	// The full name goes through.
	v4, _ := pressRunesIn(v, filesFrame(), snap, "files-one")
	_, msgs = pressIn(v4, filesFrame(), snap, "enter")
	var applied bool
	for _, msg := range msgs {
		if action, ok := msg.(tui.ActionMsg); ok && action.Op == core.OpApply {
			applied = true
		}
	}
	if !applied {
		t.Error("the correct name did not apply the change")
	}
}

// A change that cannot lose a world keeps the cheap confirmation. Friction in
// exactly three places means not in a fourth.
func TestANonWipeChangeKeepsTheYesNoConfirmation(t *testing.T) {
	snap := filesSnapshot(map[string]any{"MaxPlayers": 8, "WorldName": "Midgard"},
		core.SettingEdited{At: now, Server: "files-one", Key: "MaxPlayers", Value: 16})

	v, _ := pressIn(settings.New(), filesFrame(), snap, "A")
	if got := v.Render(filesFrame(), snap); strings.Contains(got, "Type the server name") {
		t.Errorf("a restart-impact change asked for a typed name:\n%s", got)
	}

	_, msgs := pressIn(v, filesFrame(), snap, "y")
	var applied bool
	for _, msg := range msgs {
		if action, ok := msg.(tui.ActionMsg); ok && action.Op == core.OpApply {
			applied = true
		}
	}
	if !applied {
		t.Error("y did not apply a non-wipe change")
	}
}

// pressRunesIn types text one key at a time into whatever has the keyboard.
func pressRunesIn(v tui.View, f tui.Frame, snap core.Snapshot, text string) (tui.View, []tea.Msg) {
	keys := make([]string, 0, len(text))
	for _, r := range text {
		keys = append(keys, string(r))
	}
	return pressIn(v, f, snap, keys...)
}
