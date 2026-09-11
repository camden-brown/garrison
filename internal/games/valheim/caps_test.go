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
