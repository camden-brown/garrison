package mods_test

import (
	"context"
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
	"github.com/camden-brown/garrison/internal/games"
	"github.com/camden-brown/garrison/internal/host"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tui"
	"github.com/camden-brown/garrison/internal/tui/comp"
	"github.com/camden-brown/garrison/internal/tui/views/mods"

	_ "github.com/camden-brown/garrison/internal/games/all"
)

var update = flag.Bool("update", false, "rewrite the .golden files")

func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii)
	os.Exit(m.Run())
}

var now = time.Date(2026, 9, 9, 21, 7, 0, 0, time.UTC)

// moddable is a game with a mod system, registered so the view has something
// to assert against without depending on a real plugin's answers.
type moddable struct {
	id      string
	ordered bool
	source  games.ModSource
}

func (g moddable) Meta() games.Meta {
	return games.Meta{ID: g.id, Name: strings.ToUpper(g.id[:1]) + g.id[1:], DefaultImage: "example/" + g.id}
}
func (g moddable) Plan(model.Instance) (model.Plan, error)      { return model.Plan{}, nil }
func (g moddable) Compile(model.Instance) ([]model.File, error) { return nil, nil }
func (g moddable) Schema() games.Schema                         { return games.Schema{} }
func (g moddable) Parse(string) model.Event                     { return model.Event{} }

func (g moddable) ModSource() games.ModSource { return g.source }
func (g moddable) LoadOrderMatters() bool     { return g.ordered }
func (g moddable) Apply(model.Instance, []games.Mod) ([]model.File, error) {
	return nil, nil
}

var _ games.Moddable = moddable{}

// plain is a game with no mod system at all, which is what the degraded form
// of the screen is for. It implements games.Game and nothing else, so the
// assertion in the view finds exactly what a real plugin without mods offers.
type plain struct{}

func (plain) Meta() games.Meta {
	return games.Meta{ID: "plain", Name: "Plainly", DefaultImage: "example/plain"}
}
func (plain) Plan(model.Instance) (model.Plan, error)      { return model.Plan{}, nil }
func (plain) Compile(model.Instance) ([]model.File, error) { return nil, nil }
func (plain) Schema() games.Schema                         { return games.Schema{} }
func (plain) Parse(string) model.Event                     { return model.Event{} }

func init() {
	games.Register(moddable{id: "ordered", ordered: true, source: games.ModSourceWorkshop})
	games.Register(moddable{id: "unordered", ordered: false, source: games.ModSourceThunderstore})
	games.Register(plain{})
}

func snapshot(game string, refs ...model.ModRef) core.Snapshot {
	inst := model.Instance{Name: "server-one", Game: game, Data: "/data", Mods: refs}
	return core.Reduce(core.Snapshot{Engine: core.Engine{OK: true}},
		core.InstancesLoaded{At: now, Instances: []model.Instance{inst}},
		core.FleetObserved{At: now, Containers: []host.Container{
			{Instance: "server-one", Game: game, State: model.StateRunning},
		}},
	)
}

func frame() tui.Frame {
	return tui.Frame{
		Width: 92, Height: 24, Theme: comp.NewTheme(false), Now: now,
		Server: "server-one", Focused: true,
	}
}

// The capability degradation DESIGN asks for: a game with no mod system gets
// a sentence in its own terms, not an empty table.
func TestAGameWithNoModSystemExplainsItself(t *testing.T) {
	v := mods.New()

	ok, why := v.Available(model.Instance{Name: "server-one", Game: "plain"})
	if ok {
		t.Fatal("a game implementing no games.Moddable was offered a table")
	}
	if !strings.Contains(why, "Plainly") {
		t.Errorf("the refusal = %q, want it in the game's own terms", why)
	}
}

func TestAModdableGameIsAvailable(t *testing.T) {
	if ok, why := mods.New().Available(model.Instance{Name: "server-one", Game: "ordered"}); !ok {
		t.Errorf("a moddable game was refused: %q", why)
	}
}

func TestNeedsAServer(t *testing.T) {
	if ok, why := mods.New().Available(model.Instance{}); ok || why == "" {
		t.Errorf("no server should refuse and say why: ok=%v why=%q", ok, why)
	}
}

func TestAnUnknownGameIsNamed(t *testing.T) {
	_, why := mods.New().Available(model.Instance{Name: "x", Game: "nosuchgame"})
	if !strings.Contains(why, "nosuchgame") {
		t.Errorf("the refusal = %q, want it to name the missing plugin", why)
	}
}

func TestModsAreListedInConfiguredOrder(t *testing.T) {
	snap := snapshot("ordered",
		model.ModRef{ID: "2822286426", Pin: "2.11.0"},
		model.ModRef{ID: "1234567890"},
	)

	got := mods.New().Render(frame(), snap)
	first := strings.Index(got, "2822286426")
	second := strings.Index(got, "1234567890")

	if first < 0 || second < 0 {
		t.Fatalf("not every mod is listed:\n%s", got)
	}
	if first > second {
		t.Errorf("mods are not in configured order:\n%s", got)
	}
	if !strings.Contains(got, "2.11.0") {
		t.Errorf("a pinned version is not shown:\n%s", got)
	}
	if !strings.Contains(got, "latest") {
		t.Errorf("an unpinned mod does not say it tracks latest:\n%s", got)
	}
}

// The position is shown only when it means something. A number beside a list
// whose order is irrelevant is a number somebody will try to change.
func TestLoadOrderIsNumberedOnlyWhenItMatters(t *testing.T) {
	ordered := mods.New().Render(frame(), snapshot("ordered", model.ModRef{ID: "a"}, model.ModRef{ID: "b"}))
	if !strings.Contains(ordered, "1.") || !strings.Contains(ordered, "2.") {
		t.Errorf("an ordered mod list is not numbered:\n%s", ordered)
	}
	if !strings.Contains(ordered, "load order matters") {
		t.Errorf("an ordered list does not say so:\n%s", ordered)
	}

	unordered := mods.New().Render(frame(), snapshot("unordered", model.ModRef{ID: "a"}, model.ModRef{ID: "b"}))
	if strings.Contains(unordered, "1.") {
		t.Errorf("an unordered mod list is numbered:\n%s", unordered)
	}
}

func TestTheModSourceIsNamed(t *testing.T) {
	workshop := mods.New().Render(frame(), snapshot("ordered", model.ModRef{ID: "a"}))
	if !strings.Contains(workshop, "Steam Workshop") {
		t.Errorf("the workshop source is not named:\n%s", workshop)
	}

	thunderstore := mods.New().Render(frame(), snapshot("unordered", model.ModRef{ID: "a"}))
	if !strings.Contains(thunderstore, "Thunderstore") {
		t.Errorf("the thunderstore source is not named:\n%s", thunderstore)
	}
}

// No mods and no configuration are different answers.
func TestEmptyStatesSayWhichEmptyTheyAre(t *testing.T) {
	none := mods.New().Render(frame(), snapshot("ordered"))
	if !strings.Contains(none, "No mods configured") {
		t.Errorf("a configured server with no mods should say so:\n%s", none)
	}

	// A container found by label with no file behind it.
	unconfigured := core.Reduce(core.Snapshot{Engine: core.Engine{OK: true}},
		core.FleetObserved{At: now, Containers: []host.Container{
			{Instance: "server-one", Game: "ordered", State: model.StateRunning},
		}},
	)
	got := mods.New().Render(frame(), unconfigured)
	if !strings.Contains(got, "no configuration") {
		t.Errorf("an unconfigured server should say Garrison cannot know its mods:\n%s", got)
	}
}

func TestNoLineExceedsTheFrame(t *testing.T) {
	snap := snapshot("ordered",
		model.ModRef{ID: strings.Repeat("averylongmodidentifier", 4), Pin: "1.0.0"},
	)

	for _, width := range []int{70, 92, 120} {
		f := frame()
		f.Width = width
		for i, line := range strings.Split(mods.New().Render(f, snap), "\n") {
			if w := comp.Width(line); w > width {
				t.Errorf("width %d: line %d is %d cells", width, i, w)
			}
		}
	}
}

// The whole point of the capability split: this view holds no list of which
// games have mods, so registering a new one changes nothing here.
func TestTheViewHoldsNoGameKnowledge(t *testing.T) {
	_ = context.Background()
	for _, g := range games.All() {
		inst := model.Instance{Name: "server-one", Game: g.Meta().ID}
		ok, why := mods.New().Available(inst)

		_, moddable := g.(games.Moddable)
		if ok != moddable {
			t.Errorf("%s: available=%v but Moddable=%v (%q)", g.Meta().ID, ok, moddable, why)
		}
	}
}

func TestGoldenRenders(t *testing.T) {
	tests := []struct {
		name          string
		width, height int
		snap          core.Snapshot
	}{
		{
			name: "ordered",
			snap: snapshot("ordered",
				model.ModRef{ID: "2822286426", Pin: "2.11.0"},
				model.ModRef{ID: "2392709985"},
				model.ModRef{ID: "1299328280", Pin: "4.0.1"},
			),
		},
		{
			name: "unordered",
			snap: snapshot("unordered",
				model.ModRef{ID: "ValheimModding-Jotunn", Pin: "2.19.0"},
				model.ModRef{ID: "denikson-BepInExPack_Valheim"},
			),
		},
		{name: "empty", snap: snapshot("ordered")},
		{
			// What the screen looks like once a resolver has run: names
			// instead of ids, a size, and a badge on the one that moved on.
			name: "resolved",
			snap: resolvedSnapshot("ordered",
				[]model.ModRef{{ID: "2822286426"}, {ID: "1299328280", Pin: "4.0.1"}, {ID: "9999999999"}},
				[]model.Mod{
					{ID: "2822286426", Name: "Hydrocraft", Version: "2026-01-01",
						Available: "2026-09-01", SizeBytes: 41 << 20},
					{ID: "1299328280", Pin: "4.0.1", Name: "Brita's Weapons", SizeBytes: 512 << 20},
					{ID: "9999999999", Err: "the Workshop has no visible item with this id"},
				}),
		},
		{
			name: "narrow", width: 80, height: 24,
			snap: snapshot("ordered", model.ModRef{ID: "2822286426", Pin: "2.11.0"}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := frame()
			if tt.width > 0 {
				f.Width, f.Height = tt.width, tt.height
			}

			got := mods.New().Render(f, tt.snap)
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

// resolvedSnapshot is a fleet whose mods a resolver has looked up.
func resolvedSnapshot(game string, refs []model.ModRef, resolved []model.Mod) core.Snapshot {
	s := snapshot(game, refs...)
	return core.Reduce(s, core.ModsResolved{At: now, Server: "server-one", Mods: resolved})
}

func pressMods(v tui.View, snap core.Snapshot, keys ...string) (tui.View, []tea.Msg) {
	var msgs []tea.Msg
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
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

// Reordering is the point for a game that loads mods in order: a dependency
// after its dependent is a server that will not start.
func TestReorderingMovesAMod(t *testing.T) {
	snap := snapshot("ordered",
		model.ModRef{ID: "first"}, model.ModRef{ID: "second"}, model.ModRef{ID: "third"})

	// Down to the second mod, then raise it.
	v, _ := pressMods(mods.New(), snap, "down")
	_, msgs := pressMods(v, snap, "K")

	var moved *tui.ReorderMsg
	for _, m := range msgs {
		if r, ok := m.(tui.ReorderMsg); ok {
			moved = &r
		}
	}
	if moved == nil {
		t.Fatal("K moved nothing")
	}
	if moved.From != 1 || moved.To != 0 {
		t.Errorf("moved %d -> %d, want 1 -> 0", moved.From, moved.To)
	}
	if moved.Server != "server-one" {
		t.Errorf("moved on %q", moved.Server)
	}
}

// A key that rewrites config and changes nothing is worse than no key, so it
// is not offered for a game that loads mods in whatever order it likes.
func TestNoReorderingWhereOrderDoesNotMatter(t *testing.T) {
	snap := snapshot("unordered", model.ModRef{ID: "a"}, model.ModRef{ID: "b"})

	_, msgs := pressMods(mods.New(), snap, "K")
	for _, m := range msgs {
		if _, ok := m.(tui.ReorderMsg); ok {
			t.Error("an unordered game offered reordering")
		}
	}
}

func TestReorderingStopsAtTheEnds(t *testing.T) {
	snap := snapshot("ordered", model.ModRef{ID: "a"}, model.ModRef{ID: "b"})

	// The first mod cannot go earlier.
	_, msgs := pressMods(mods.New(), snap, "K")
	for _, m := range msgs {
		if _, ok := m.(tui.ReorderMsg); ok {
			t.Error("the first mod was moved earlier")
		}
	}

	// Nor the last later.
	v, _ := pressMods(mods.New(), snap, "down")
	_, msgs = pressMods(v, snap, "J")
	for _, m := range msgs {
		if _, ok := m.(tui.ReorderMsg); ok {
			t.Error("the last mod was moved later")
		}
	}
}

// The cursor follows the mod, so holding K walks one mod up the list rather
// than walking the list past the cursor.
func TestTheCursorFollowsTheMovedMod(t *testing.T) {
	snap := snapshot("ordered",
		model.ModRef{ID: "first"}, model.ModRef{ID: "second"}, model.ModRef{ID: "third"})

	v, _ := pressMods(mods.New(), snap, "down", "down") // on "third"
	v, _ = pressMods(v, snap, "K")

	// The snapshot has not moved — the store would have — so the cursor
	// should now be on index 1, which is still "second" in this fixture.
	// What matters is that it moved with the intent rather than staying.
	_, msgs := pressMods(v, snap, "K")
	var second *tui.ReorderMsg
	for _, m := range msgs {
		if r, ok := m.(tui.ReorderMsg); ok {
			second = &r
		}
	}
	if second == nil {
		t.Fatal("the second K moved nothing")
	}
	if second.From != 1 {
		t.Errorf("the second move was from %d, want 1 — the cursor did not follow", second.From)
	}
}

// A resolver turns a column of ids into a list a person can read.
func TestResolvedModsShowTheirNames(t *testing.T) {
	snap := resolvedSnapshot("ordered",
		[]model.ModRef{{ID: "2822286426"}},
		[]model.Mod{{ID: "2822286426", Name: "Hydrocraft", SizeBytes: 4 << 20}},
	)

	got := mods.New().Render(frame(), snap)
	if !strings.Contains(got, "Hydrocraft") {
		t.Errorf("the resolved name is missing:\n%s", got)
	}
	if !strings.Contains(got, "MiB") {
		t.Errorf("the size is missing:\n%s", got)
	}
}

// The id is what the operator wrote and never stops being true, so it is the
// fallback rather than a blank row.
func TestUnresolvedModsStillShowTheirID(t *testing.T) {
	snap := snapshot("ordered", model.ModRef{ID: "2822286426"})

	if got := mods.New().Render(frame(), snap); !strings.Contains(got, "2822286426") {
		t.Errorf("an unresolved mod vanished:\n%s", got)
	}
}

func TestAnUpdateIsFlagged(t *testing.T) {
	snap := resolvedSnapshot("ordered",
		[]model.ModRef{{ID: "1"}},
		[]model.Mod{{ID: "1", Name: "Old", Version: "2026-01-01", Available: "2026-09-01"}},
	)

	if got := mods.New().Render(frame(), snap); !strings.Contains(got, "update available") {
		t.Errorf("an out-of-date mod is not flagged:\n%s", got)
	}
}

// The operator wrote that id themselves and the answer is usually a typo.
func TestAnUnknownModSaysSo(t *testing.T) {
	snap := resolvedSnapshot("ordered",
		[]model.ModRef{{ID: "9999999999"}},
		[]model.Mod{{ID: "9999999999", Err: "the Workshop has no visible item with this id"}},
	)

	if got := mods.New().Render(frame(), snap); !strings.Contains(got, "no visible item") {
		t.Errorf("an unknown mod is not reported:\n%s", got)
	}
}

// A pin is the operator saying which version they want; a badge nagging about
// a deliberate choice is noise.
func TestAPinnedModIsNotNagged(t *testing.T) {
	snap := resolvedSnapshot("ordered",
		[]model.ModRef{{ID: "1", Pin: "2.11.0"}},
		[]model.Mod{{ID: "1", Pin: "2.11.0", Name: "Pinned", Version: "2.11.0", Available: "2026-09-01"}},
	)

	if got := mods.New().Render(frame(), snap); strings.Contains(got, "update available") {
		t.Errorf("a pinned mod was nagged:\n%s", got)
	}
}
