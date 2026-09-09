package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/config"
	"github.com/camden-brown/garrison/internal/model"
)

// A server file is a contract with a person: they can edit it with the tool
// closed, so this is a real file somebody could plausibly have typed.
const handWritten = `
game  = "zomboid"
image = "docker.io/cyrale/project-zomboid:latest"
data  = 'D:\gameservers\zomboid-main\data'

[resources]
memory = "6GiB"
cpus   = 2.5

[[ports]]
container = "16261/udp"
host      = 16261

[[ports]]
container = "16262/udp"
host      = 16262

[settings]
MaxPlayers   = 16
PVP          = false
ServerName   = "Knox County"

[[mods]]
id  = "2822286426"
pin = "1.4.2"

[[mods]]
id = "2392709985"

[[schedule]]
kind   = "restart"
cron   = "0 4 * * *"
drain  = "15m"
policy = "skip-if-occupied"
`

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
}

func TestLoadAHandWrittenFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "zomboid-main", handWritten)

	inst, err := config.Store{Dir: dir}.Load("zomboid-main")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if inst.Name != "zomboid-main" {
		t.Errorf("Name = %q — the name comes from the filename, not the file", inst.Name)
	}
	if inst.Game != "zomboid" {
		t.Errorf("Game = %q", inst.Game)
	}
	if want := int64(6) << 30; inst.Resources.Memory != want {
		t.Errorf("Memory = %d, want %d — 6GiB", inst.Resources.Memory, want)
	}
	if inst.Resources.CPUs != 2.5 {
		t.Errorf("CPUs = %v", inst.Resources.CPUs)
	}
	if len(inst.Ports) != 2 || inst.Ports[0].Container != "16261/udp" {
		t.Errorf("Ports = %+v", inst.Ports)
	}
	if got := inst.Settings["MaxPlayers"]; got != int64(16) {
		t.Errorf("MaxPlayers = %v (%T), want 16", got, got)
	}
	if len(inst.Mods) != 2 || inst.Mods[0].Pin != "1.4.2" {
		t.Errorf("Mods = %+v", inst.Mods)
	}
	if len(inst.Schedules) != 1 || inst.Schedules[0].Drain != 15*time.Minute {
		t.Errorf("Schedules = %+v", inst.Schedules)
	}
}

// Load then save then load must not change anything. A settings apply rewrites
// the file, and a round trip that quietly drops a field somebody hand-added is
// a configuration that decays every time it is touched.
func TestRoundTripPreservesEverything(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "zomboid-main", handWritten)
	store := config.Store{Dir: dir}

	first, err := store.Load("zomboid-main")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := store.Save(first); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	second, err := store.Load("zomboid-main")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	if first.Game != second.Game || first.Image != second.Image || first.Data != second.Data {
		t.Errorf("identity changed:\n%+v\n%+v", first, second)
	}
	if first.Resources != second.Resources {
		t.Errorf("resources changed: %+v vs %+v", first.Resources, second.Resources)
	}
	if len(first.Ports) != len(second.Ports) || len(first.Mods) != len(second.Mods) {
		t.Errorf("ports or mods were dropped")
	}
	if len(first.Schedules) != len(second.Schedules) || first.Schedules[0].Drain != second.Schedules[0].Drain {
		t.Errorf("schedules changed: %+v vs %+v", first.Schedules, second.Schedules)
	}
	for k, v := range first.Settings {
		if second.Settings[k] != v {
			t.Errorf("setting %q changed: %v -> %v", k, v, second.Settings[k])
		}
	}
}

// The file stays readable. Somebody opening it to fix a typo should recognise
// what they wrote.
func TestSavedFileIsReadable(t *testing.T) {
	dir := t.TempDir()
	store := config.Store{Dir: dir}

	err := store.Save(model.Instance{
		Name: "valheim-huldra", Game: "valheim",
		Resources: model.Resources{Memory: 4 << 30},
		Ports:     []model.PortMap{{Container: "2456/udp", Host: 2456}},
		Settings:  map[string]any{"ServerName": "Huldra"},
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dir, "valheim-huldra.toml"))
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{`game = "valheim"`, `memory = "4GiB"`, "ServerName", "2456/udp"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("saved file does not contain %q:\n%s", want, body)
		}
	}
	// A byte count nobody can read defeats the point of a hand-editable file.
	if strings.Contains(string(body), "4294967296") {
		t.Errorf("memory was written as raw bytes:\n%s", body)
	}
}

// An interrupted write must cost the change, not the server. The previous file
// stays intact until the new one is complete.
func TestSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "zomboid-main", handWritten)
	store := config.Store{Dir: dir}

	before, _ := os.ReadFile(store.Path("zomboid-main"))

	// A value TOML cannot encode fails mid-save.
	err := store.Save(model.Instance{
		Name: "zomboid-main", Game: "zomboid",
		Settings: map[string]any{"bad": make(chan int)},
	})
	if err == nil {
		t.Fatal("Save() succeeded with an unencodable value")
	}

	after, _ := os.ReadFile(store.Path("zomboid-main"))
	if string(before) != string(after) {
		t.Error("a failed save damaged the existing file")
	}

	// And it must not leave its scratch file behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}

// One malformed server must not take the whole dashboard down.
func TestOneBadFileDoesNotStopTheOthers(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "good", `game = "valheim"`)
	write(t, dir, "broken", `game = "valheim"`+"\nthis is not toml [[[")
	write(t, dir, "nogame", `image = "x"`)

	got, problems := config.Store{Dir: dir}.LoadAll()

	if len(got) != 1 || got[0].Name != "good" {
		t.Errorf("loaded %+v, want just the good one", got)
	}
	if len(problems) != 2 {
		t.Errorf("got %d problems, want 2", len(problems))
	}
	for _, err := range problems {
		if !strings.Contains(err.Error(), "broken") && !strings.Contains(err.Error(), "nogame") {
			t.Errorf("problem does not name its file: %v", err)
		}
	}
}

// A game nobody declared means Garrison cannot tell which plugin owns the
// server, which is worth saying rather than defaulting to something.
func TestMissingGameIsAnError(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x", `image = "whatever"`)

	if _, err := (config.Store{Dir: dir}).Load("x"); err == nil {
		t.Fatal("Load() error = nil for a file with no game")
	} else if !strings.Contains(err.Error(), "game") {
		t.Errorf("error = %v, want it to name the missing field", err)
	}
}

func TestMissingFile(t *testing.T) {
	_, err := (config.Store{Dir: t.TempDir()}).Load("ghost")
	if !errors.Is(err, config.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

// A first run has no directory, which is not a problem to report.
func TestNoDirectoryIsAFirstRunNotAFailure(t *testing.T) {
	got, problems := config.Store{Dir: filepath.Join(t.TempDir(), "nope")}.LoadAll()
	if len(got) != 0 || len(problems) != 0 {
		t.Errorf("LoadAll() on a missing directory = %v, %v", got, problems)
	}
}

func TestLoadAllIsSortedByName(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"zomboid", "alpha", "mid"} {
		write(t, dir, name, `game = "valheim"`)
	}

	got, _ := config.Store{Dir: dir}.LoadAll()
	for i, want := range []string{"alpha", "mid", "zomboid"} {
		if got[i].Name != want {
			t.Errorf("position %d = %q, want %q", i, got[i].Name, want)
		}
	}
}

func TestDelete(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "x", `game = "valheim"`)
	store := config.Store{Dir: dir}

	if err := store.Delete("x"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if err := store.Delete("x"); err != nil {
		t.Errorf("deleting a missing file is an error: %v", err)
	}
}
