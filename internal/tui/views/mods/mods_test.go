package mods_test

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
// to assert against. Valheim implements no games.Moddable, which is the honest
// state of it — so without this the screen could only ever be tested in its
// degraded form.
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

func init() {
	games.Register(moddable{id: "ordered", ordered: true, source: games.ModSourceWorkshop})
	games.Register(moddable{id: "unordered", ordered: false, source: games.ModSourceThunderstore})
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

	ok, why := v.Available(model.Instance{Name: "valheim-huldra", Game: "valheim"})
	if ok {
		t.Fatal("Valheim implements no games.Moddable but the view offered a table")
	}
	if !strings.Contains(why, "Valheim") {
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
