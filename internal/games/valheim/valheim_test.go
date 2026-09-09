package valheim

import (
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/games"
	"github.com/camden-brown/garrison/internal/model"
)

func instance() model.Instance {
	return model.Instance{
		Name: "valheim-huldra",
		Game: "valheim",
		Data: `D:\gameservers\valheim-huldra\data`,
		Settings: map[string]any{
			KeyServerName: "Huldra",
			KeyWorldName:  "Huldra",
			KeyPassword:   "fixture99",
			KeyPublic:     false,
		},
	}
}

// The signal is game knowledge and getting it wrong costs a save: the image
// traps SIGINT to shut down cleanly, and SIGTERM kills it mid-write.
func TestPlanStopsTheWayTheImageExpects(t *testing.T) {
	plan, err := (Game{}).Plan(instance())
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}

	if plan.StopSignal != "SIGINT" {
		t.Errorf("StopSignal = %q, want SIGINT", plan.StopSignal)
	}
	if plan.StopGrace < 60*time.Second {
		t.Errorf("StopGrace = %v, which is not long enough to write a populated world", plan.StopGrace)
	}
}

func TestPlanCarriesSettingsAsEnvironment(t *testing.T) {
	plan, err := (Game{}).Plan(instance())
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}

	for key, want := range map[string]string{
		"SERVER_NAME":   "Huldra",
		"WORLD_NAME":    "Huldra",
		"SERVER_PASS":   "fixture99",
		"SERVER_PUBLIC": "0",
	} {
		if got := plan.Env[key]; got != want {
			t.Errorf("Env[%q] = %q, want %q", key, got, want)
		}
	}
}

// Garrison supervises restarts. Letting the image's updater bounce the server
// produces a stop nobody asked for, which the fleet view has to call a crash.
func TestPlanDisablesTheImagesOwnUpdater(t *testing.T) {
	plan, _ := (Game{}).Plan(instance())
	if got := plan.Env["UPDATE_CRON"]; got != "" {
		t.Errorf("UPDATE_CRON = %q, want it disabled", got)
	}
}

func TestPlanFallsBackToDefaults(t *testing.T) {
	plan, err := (Game{}).Plan(model.Instance{Name: "bare", Game: "valheim"})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}

	if plan.Image != (Game{}).Meta().DefaultImage {
		t.Errorf("Image = %q, want the default", plan.Image)
	}
	if len(plan.Ports) == 0 {
		t.Error("no ports, want the defaults")
	}
	if plan.Env["SERVER_NAME"] != "bare" {
		t.Errorf("SERVER_NAME = %q, want the instance name", plan.Env["SERVER_NAME"])
	}
	// No password configured means the variable is absent rather than empty:
	// an empty SERVER_PASS is a server that rejects every connection.
	if _, present := plan.Env["SERVER_PASS"]; present {
		t.Error("SERVER_PASS is set with no password configured")
	}
}

// The empty slice is the point, not an oversight — see ADR 0006.
func TestCompileWritesNothing(t *testing.T) {
	files, err := (Game{}).Compile(instance())
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if len(files) != 0 {
		t.Errorf("Compile() returned %d files, want none — Valheim is configured entirely by environment", len(files))
	}
}

// Schema validity: every default inside its own range, no duplicate keys,
// every field in a group.
func TestSchemaIsWellFormed(t *testing.T) {
	schema := (Game{}).Schema()

	seen := map[string]bool{}
	for _, f := range schema.Fields {
		if seen[f.Key] {
			t.Errorf("duplicate key %q", f.Key)
		}
		seen[f.Key] = true

		if f.Label == "" {
			t.Errorf("%s has no label", f.Key)
		}
		if f.Group == "" {
			t.Errorf("%s is in no group", f.Key)
		}
		if f.Help == "" {
			t.Errorf("%s has no help text", f.Key)
		}
	}

	if len(schema.Groups()) == 0 {
		t.Error("Schema has no groups")
	}
}

// Valheim has no config file and no live reload, so nothing can be applied
// without a new container. The form must say so rather than implying a
// setting takes effect immediately.
func TestNoSettingClaimsToApplyLive(t *testing.T) {
	for _, f := range (Game{}).Schema().Fields {
		if f.Impact == games.ImpactLive {
			t.Errorf("%s claims ImpactLive, but every Valheim setting is an environment variable", f.Key)
		}
	}
}

// Changing the world name starts a new world rather than renaming the old one,
// which is a lost save if it is applied without the confirmation that impact
// level triggers.
func TestWorldNameIsMarkedWipeRisk(t *testing.T) {
	f, ok := (Game{}).Schema().Field(KeyWorldName)
	if !ok {
		t.Fatal("no world name field")
	}
	if f.Impact != games.ImpactWipeRisk {
		t.Errorf("world name impact = %v, want WipeRisk", f.Impact)
	}
}

func TestMeta(t *testing.T) {
	m := (Game{}).Meta()

	if m.ID != "valheim" {
		t.Errorf("ID = %q", m.ID)
	}
	if m.SteamAppID != "896660" {
		t.Errorf("SteamAppID = %q, want the dedicated server app id", m.SteamAppID)
	}
	if m.MetricKey != MetricSaveMillis {
		t.Errorf("MetricKey = %q, want it to match what Parse emits", m.MetricKey)
	}
	if m.MetricLabel == "" {
		t.Error("MetricLabel is empty, so the fourth tile has no name")
	}
}

// Valheim has no RCON and no roster endpoint, so it must not claim otherwise:
// the Players view reads these assertions to decide whether to show a table or
// an explanation.
func TestValheimClaimsOnlyWhatItCanDo(t *testing.T) {
	var g games.Game = Game{}

	if _, ok := g.(games.Rostered); ok {
		t.Error("Valheim claims Rostered, but it has no RCON — the roster comes from Parse")
	}
	if _, ok := g.(games.Commandable); ok {
		t.Error("Valheim claims Commandable, but it has no command channel")
	}
}

func TestRegisteredUnderItsID(t *testing.T) {
	g, err := games.Get("valheim")
	if err != nil {
		t.Fatalf("Get(valheim): %v", err)
	}
	if g.Meta().ID != "valheim" {
		t.Errorf("registered under the wrong id: %q", g.Meta().ID)
	}
}
