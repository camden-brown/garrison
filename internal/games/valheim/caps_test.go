package valheim

import (
	"strings"
	"testing"

	"github.com/camden-brown/garrison/internal/games"
	"github.com/camden-brown/garrison/internal/model"
)

// The loader and the mods are one fact. A server with plugins configured and
// no BepInEx runs vanilla and loads none of them, which looks exactly like
// mods that do not work.
func TestBepInExFollowsTheModList(t *testing.T) {
	bare, err := (Game{}).Plan(instance())
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if bare.Env["BEPINEX"] != "false" {
		t.Errorf("BEPINEX = %q with no mods, want false", bare.Env["BEPINEX"])
	}

	inst := instance()
	inst.Mods = []model.ModRef{{ID: "ValheimModding-Jotunn"}}
	modded, err := (Game{}).Plan(inst)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if modded.Env["BEPINEX"] != "true" {
		t.Errorf("BEPINEX = %q with a mod configured, want true", modded.Env["BEPINEX"])
	}
}

// The image compares these against the words. "1" is what SERVER_PUBLIC
// wants and it is not the same switch.
func TestTheLoaderFlagIsTheWordTheImageReads(t *testing.T) {
	inst := instance()
	inst.Mods = []model.ModRef{{ID: "Some-Mod"}}
	plan, _ := (Game{}).Plan(inst)
	if got := plan.Env["BEPINEX"]; got != "true" && got != "false" {
		t.Errorf("BEPINEX = %q, want true or false", got)
	}
}

// ModDir is relative to the instance's data, like model.File, so a plugin
// never learns where the world is mounted.
func TestModDirIsTheImagesStagingDirectory(t *testing.T) {
	dir := (Game{}).ModDir(instance())
	if dir != "bepinex/plugins" {
		t.Errorf("ModDir() = %q, want the directory the image syncs into the loader", dir)
	}
}

// Installing the loader as if it were a mod leaves two of them arguing over
// the same doorstop entry point.
func TestTheBepInExPackIsNotAMod(t *testing.T) {
	bundled := (Game{}).Bundled()
	if len(bundled) == 0 {
		t.Fatal("Bundled() is empty, so Garrison would install the loader under the loader")
	}
	found := false
	for _, id := range bundled {
		if id == "denikson-BepInExPack_Valheim" {
			found = true
		}
	}
	if !found {
		t.Errorf("Bundled() = %v, want the pack the image downloads", bundled)
	}
}

// Valheim's mods are files and nothing in its configuration names one, which
// is the whole reason Installable had to exist.
func TestApplyDeclaresNothing(t *testing.T) {
	files, err := (Game{}).Apply(instance(), []games.Mod{{ID: "Some-Mod", Enabled: true}})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(files) != 0 {
		t.Errorf("Apply() wrote %d files, want none — no Valheim config names a mod", len(files))
	}
}

// The bug this closes: the image syncs the staged plugins into the loader
// only if the loader's plugins directory already exists, and on the boot that
// installs the loader it does not. Every recreate therefore ran the server
// with the mods staged and none loaded — "0 plugins to load", with the DLL
// sitting correctly on disk a directory away.
func TestAModdedServerSyncsItsPluginsBeforeItStarts(t *testing.T) {
	inst := instance()
	inst.Mods = []model.ModRef{{ID: "Azumatt-AzuExtendedPlayerInventory"}}

	plan, err := (Game{}).Plan(inst)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	hook := plan.Env["PRE_SERVER_RUN_HOOK"]
	if hook == "" {
		t.Fatal("no hook, so a freshly recreated container starts with no plugins loaded")
	}
	// Both halves have to be in it: where Garrison installed them, and
	// where the loader reads them.
	if !strings.Contains(hook, "/config/"+(Game{}).ModDir(inst)) {
		t.Errorf("hook = %q, want it to read from where mods are installed", hook)
	}
	if !strings.Contains(hook, "/opt/valheim/bepinex/BepInEx/plugins") {
		t.Errorf("hook = %q, want it to write where the loader reads", hook)
	}
}

// An unmodded server gets no hook, so its plan — and therefore its container
// — is exactly what it was before any of this existed.
func TestAnUnmoddedServerGetsNoHook(t *testing.T) {
	plan, _ := (Game{}).Plan(instance())
	if got := plan.Env["PRE_SERVER_RUN_HOOK"]; got != "" {
		t.Errorf("PRE_SERVER_RUN_HOOK = %q on a server with no mods, want none", got)
	}
}

// Every recreate used to throw away ~6 GB and refetch 2 GB of it from Steam,
// which made a one-line settings change a four-minute job. The download is
// the only part kept: the install and the loader merge are still rebuilt, so
// a recreate is still a real reset.
func TestThePlanKeepsTheDownloadAndNothingElse(t *testing.T) {
	plan, err := (Game{}).Plan(instance())
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}

	var caches []model.Mount
	for _, m := range plan.Mounts {
		if m.Cache {
			caches = append(caches, m)
		}
	}
	if len(caches) != 1 {
		t.Fatalf("%d cache mounts, want exactly the download", len(caches))
	}
	if caches[0].Container != "/opt/valheim/dl" {
		t.Errorf("cache is %q, want the download directory — the installation must be rebuilt", caches[0].Container)
	}
	if !caches[0].IsVolume() {
		t.Error("the cache is a host path; a bind mount from a Windows drive is ~32x slower for small files")
	}
	if caches[0].Volume != instance().CacheVolume("dl") {
		t.Errorf("volume = %q, want one named for the instance", caches[0].Volume)
	}

	// And the world is still mounted, unmarked, where it always was.
	var world model.Mount
	for _, m := range plan.Mounts {
		if m.Container == "/config" {
			world = m
		}
	}
	if world.Container == "" {
		t.Fatal("the world is not mounted at all")
	}
	if world.Cache {
		t.Fatal("the world is marked as a cache, which is a delete that removes somebody's save")
	}
}

// An admin is the only player who can run devcommands, which is the only way
// to unstick a character or undo what somebody built. Without this the list
// could only be written by hand, into a file the image rewrites.
func TestAdminsReachTheImage(t *testing.T) {
	inst := instance()
	inst.Settings[KeyAdmins] = "76561198077609761 76561198006139107"

	plan, err := (Game{}).Plan(inst)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if got := plan.Env["ADMINLIST_IDS"]; got != "76561198077609761 76561198006139107" {
		t.Errorf("ADMINLIST_IDS = %q, want both ids space-separated", got)
	}
}

// A list is something somebody pastes from Discord, and a comma between two
// ids must not produce one admin whose id is both numbers joined together.
func TestAdminsAreSplitOnWhateverSeparatorWasPasted(t *testing.T) {
	inst := instance()
	inst.Settings[KeyAdmins] = "76561198077609761,76561198006139107 , 76561198000000000"

	plan, _ := (Game{}).Plan(inst)
	if got := plan.Env["ADMINLIST_IDS"]; got != "76561198077609761 76561198006139107 76561198000000000" {
		t.Errorf("ADMINLIST_IDS = %q, want three ids separated by single spaces", got)
	}
}

// Unset is not the same as nobody. The image rewrites adminlist.txt only when
// the variable is set, so leaving it alone preserves a list written by hand —
// and emptying it silently would take away the only admin on a server at the
// moment somebody needed one.
func TestNoAdminsLeavesTheListAlone(t *testing.T) {
	plan, _ := (Game{}).Plan(instance())
	if _, present := plan.Env["ADMINLIST_IDS"]; present {
		t.Error("ADMINLIST_IDS is set with no admins configured, which rewrites the file")
	}
}
