package valheim

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/camden-brown/garrison/internal/games"
	"github.com/camden-brown/garrison/internal/model"
)

// enums reads the vocabulary captured from assembly_valheim.dll.
//
// The fixture is the game's own metadata rather than documentation, which is
// the only reason the assertions below are worth making: they compare what
// Garrison will put on a command line against what Valheim will parse off one.
func enums(t *testing.T) map[string]map[string]bool {
	t.Helper()

	f, err := os.Open("testdata/world-modifiers.txt")
	if err != nil {
		t.Fatalf("opening the extracted enums: %v", err)
	}
	defer f.Close()

	out := map[string]map[string]bool{}
	section := ""
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			section = strings.Trim(line, "[]")
			out[section] = map[string]bool{}
		case section != "":
			// Valheim parses both -modifier arguments with a
			// case-insensitive Enum.TryParse, so the comparison is too.
			out[section][strings.ToLower(line)] = true
		}
	}
	if err := scan.Err(); err != nil {
		t.Fatalf("reading the extracted enums: %v", err)
	}
	for _, want := range []string{"WorldModifiers", "WorldModifierOption", "GlobalKeys"} {
		if len(out[want]) == 0 {
			t.Fatalf("fixture has no [%s] section", want)
		}
	}
	return out
}

// A modifier Valheim cannot parse is not an error it reports. The server logs
// the value and carries on with it unapplied, so a typo here is a difficulty
// setting that silently never took effect.
func TestModifierKeysAreValheimsOwn(t *testing.T) {
	got := enums(t)

	for _, m := range modifiers {
		if !got["WorldModifiers"][strings.ToLower(m.key)] {
			t.Errorf("modifier %q is not a WorldModifiers member", m.key)
		}
		for _, o := range m.options {
			v, _ := o.Value.(string)
			if v == "" {
				continue // "Standard" emits no flag at all.
			}
			if !got["WorldModifierOption"][v] {
				t.Errorf("%s option %q is not a WorldModifierOption member", m.key, v)
			}
		}
	}
}

func TestToggleKeysAreGlobalKeys(t *testing.T) {
	got := enums(t)

	for _, tg := range toggles {
		if !got["GlobalKeys"][tg.globalKey] {
			t.Errorf("-setkey %q is not a GlobalKeys member", tg.globalKey)
		}
	}
}

// Every enum field the form renders has to have something to render. An enum
// with no options is a field the user can focus and never change.
func TestEnumFieldsCarryOptions(t *testing.T) {
	for _, f := range (Game{}).Schema().Fields {
		if f.Type == games.TypeEnum && len(f.Options) == 0 {
			t.Errorf("field %q is an enum with no options", f.Key)
		}
	}
}

func TestServerArgsBuildsTheLaunchLine(t *testing.T) {
	inst := model.Instance{
		Name: "valheim-huldra",
		Settings: map[string]any{
			KeyDeathPenalty: "casual",
			KeyPortals:      "casual",
			KeyNoMap:        true,
		},
	}

	got := serverArgs(inst)
	for _, want := range []string{
		"-modifier DeathPenalty casual",
		"-modifier Portals casual",
		"-setkey nomap",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("serverArgs() = %q, want it to contain %q", got, want)
		}
	}
}

// Nothing set means nothing passed. An empty SERVER_ARGS leaves the world
// with whatever modifiers it already has, which is not the same as resetting
// it to standard.
func TestServerArgsIsEmptyWhenNothingIsSet(t *testing.T) {
	if got := serverArgs(model.Instance{Name: "bare"}); got != "" {
		t.Errorf("serverArgs() = %q, want empty", got)
	}
}

// The image expands $SERVER_ARGS unquoted, so a value with a space in it
// would split into arguments of its own. Only values the schema declared are
// ever emitted, which closes that off regardless of what is in the TOML.
func TestServerArgsDropsValuesTheSchemaDoesNotDeclare(t *testing.T) {
	inst := model.Instance{
		Name: "valheim-huldra",
		Settings: map[string]any{
			KeyDeathPenalty: "casual -setkey nobuildcost",
			KeyCombat:       "impossible",
			KeyPortals:      "casual",
		},
	}

	got := serverArgs(inst)
	if strings.Contains(got, "nobuildcost") {
		t.Errorf("serverArgs() = %q, want the injected flag dropped", got)
	}
	if strings.Contains(got, "impossible") {
		t.Errorf("serverArgs() = %q, want the unknown option dropped", got)
	}
	if !strings.Contains(got, "-modifier Portals casual") {
		t.Errorf("serverArgs() = %q, want the valid modifier kept", got)
	}
	if fields := strings.Fields(got); len(fields) != 3 {
		t.Errorf("serverArgs() = %q, want exactly one modifier's three words", got)
	}
}

// Garrison is the supervisor. The image ships schedules that would restart
// the server and take backups on their own, both of which race the task
// engine and surface as events nobody asked for.
func TestPlanDisablesTheImagesOwnSchedules(t *testing.T) {
	plan, err := (Game{}).Plan(model.Instance{Name: "valheim-huldra"})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}

	for _, key := range []string{"UPDATE_CRON", "RESTART_CRON"} {
		if got := plan.Env[key]; got != "" {
			t.Errorf("Env[%q] = %q, want it disabled", key, got)
		}
	}
	if got := plan.Env["BACKUPS"]; got != "false" {
		t.Errorf(`Env["BACKUPS"] = %q, want "false" — the image would write into the directory Garrison archives`, got)
	}
}

func TestPlanCarriesModifiersAsServerArgs(t *testing.T) {
	inst := model.Instance{
		Name:     "valheim-huldra",
		Settings: map[string]any{KeyDeathPenalty: "casual"},
	}

	plan, err := (Game{}).Plan(inst)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if got := plan.Env["SERVER_ARGS"]; got != "-modifier DeathPenalty casual" {
		t.Errorf(`Env["SERVER_ARGS"] = %q, want the death penalty modifier`, got)
	}
}

func TestPlanOmitsServerArgsWhenNothingIsSet(t *testing.T) {
	plan, _ := (Game{}).Plan(model.Instance{Name: "bare"})
	if _, ok := plan.Env["SERVER_ARGS"]; ok {
		t.Error("SERVER_ARGS is set with no modifiers configured, want it absent")
	}
}
