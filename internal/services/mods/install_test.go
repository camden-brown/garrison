package mods_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/services/mods"
)

// pkgZip builds a package the way Thunderstore packages are actually built:
// content under plugins/, the packaging files Thunderstore requires at the
// root, and a BepInEx config the server must keep for itself.
func pkgZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("building the zip: %v", err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("building the zip: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("building the zip: %v", err)
	}
	return buf.Bytes()
}

// layout is Valheim's, which is the only shape that exists so far: plugins
// and patchers under the instance's data.
var layout = mods.Layout{Plugins: "bepinex/plugins", Patchers: "bepinex/patchers", Config: "bepinex"}

// releases is a Releaser with no network behind it.
type releases map[string]mods.Release

func (r releases) Release(_ context.Context, ref model.ModRef) (mods.Release, error) {
	rel, ok := r[ref.ID]
	if !ok {
		return mods.Release{}, errors.New("no such package: " + ref.ID)
	}
	if ref.Pin != "" {
		rel.Version = ref.Pin
	}
	return rel, nil
}

// zipServer serves each package's bytes at its own path.
func zipServer(t *testing.T, byPath map[string][]byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := byPath[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestInstallUnpacksAPackage(t *testing.T) {
	dir := t.TempDir()
	zipped := pkgZip(t, map[string]string{
		"plugins/Jotunn.dll":       "assembly",
		"plugins/sub/asset.bundle": "asset",
		"manifest.json":            "{}",
		"README.md":                "read me",
		"BepInEx/config/x.cfg":     "do not overwrite the operator's config",
	})
	srv := zipServer(t, map[string][]byte{"/jotunn.zip": zipped})

	install := mods.Install{
		Root:   dir,
		Layout: layout,
		Source: releases{"ValheimModding-Jotunn": {ID: "ValheimModding-Jotunn", Version: "2.30.0", URL: srv.URL + "/jotunn.zip"}},
	}

	if _, err := install.Sync(context.Background(), []model.ModRef{{ID: "ValheimModding-Jotunn"}}, nil); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	// One directory per mod, which is what makes pruning exact and what
	// BepInEx walks anyway.
	if got := read(t, filepath.Join(dir, "bepinex", "plugins", "ValheimModding-Jotunn", "Jotunn.dll")); got != "assembly" {
		t.Errorf("the plugin did not land: %q", got)
	}
	if got := read(t, filepath.Join(dir, "bepinex", "plugins", "ValheimModding-Jotunn", "sub", "asset.bundle")); got != "asset" {
		t.Errorf("a nested file did not land: %q", got)
	}
	for _, unwanted := range []string{"manifest.json", "README.md", filepath.Join("BepInEx", "config", "x.cfg")} {
		if _, err := os.Stat(filepath.Join(dir, "bepinex", "plugins", "ValheimModding-Jotunn", unwanted)); err == nil {
			t.Errorf("%s was installed, and it is not plugin content", unwanted)
		}
	}

	if v := install.Installed("ValheimModding-Jotunn").Version; v != "2.30.0" {
		t.Errorf("Installed().Version = %q, want the version that was fetched", v)
	}
	// The URL form is the same mod under a different spelling.
	if v := install.Installed("ValheimModding/Jotunn").Version; v != "2.30.0" {
		t.Errorf("the slash form resolved to %q, want the same mod", v)
	}
}

// The whole reason for the manifest: this directory is also somewhere a
// person drops a DLL by hand, and a sync must not eat it.
func TestSyncLeavesFilesGarrisonDidNotInstall(t *testing.T) {
	dir := t.TempDir()
	byHand := filepath.Join(dir, "HandDropped")
	if err := os.MkdirAll(byHand, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(byHand, "Mine.dll"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	zipped := pkgZip(t, map[string]string{"plugins/A.dll": "a"})
	srv := zipServer(t, map[string][]byte{"/a.zip": zipped})
	install := mods.Install{
		Root:   dir,
		Layout: layout,
		Source: releases{"Some-Mod": {ID: "Some-Mod", Version: "1.0.0", URL: srv.URL + "/a.zip"}},
	}

	if _, err := install.Sync(context.Background(), []model.ModRef{{ID: "Some-Mod"}}, nil); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	// And again with nothing configured, which prunes.
	if _, err := install.Sync(context.Background(), nil, nil); err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "bepinex", "plugins", "Some-Mod")); err == nil {
		t.Error("the configured mod was not removed once it stopped being configured")
	}
	if got := read(t, filepath.Join(byHand, "Mine.dll")); got != "mine" {
		t.Errorf("a hand-dropped plugin was disturbed: %q", got)
	}
}

// An apply that installs mods and then cannot start the server must leave the
// server as it found it, which is what the step's compensation calls.
func TestUndoPutsBackWhatWasThere(t *testing.T) {
	dir := t.TempDir()
	old := pkgZip(t, map[string]string{"plugins/A.dll": "old"})
	updated := pkgZip(t, map[string]string{"plugins/A.dll": "new"})
	srv := zipServer(t, map[string][]byte{"/old.zip": old, "/new.zip": updated})

	first := mods.Install{
		Root:   dir,
		Layout: layout,
		Source: releases{"Some-Mod": {ID: "Some-Mod", Version: "1.0.0", URL: srv.URL + "/old.zip"}},
	}
	if _, err := first.Sync(context.Background(), []model.ModRef{{ID: "Some-Mod"}}, nil); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	second := mods.Install{
		Root:   dir,
		Layout: layout,
		Source: releases{
			"Some-Mod":  {ID: "Some-Mod", Version: "2.0.0", URL: srv.URL + "/new.zip"},
			"Other-Mod": {ID: "Other-Mod", Version: "1.0.0", URL: srv.URL + "/new.zip"},
		},
	}
	undo, err := second.Sync(context.Background(), []model.ModRef{{ID: "Some-Mod"}, {ID: "Other-Mod"}}, nil)
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if got := read(t, filepath.Join(dir, "bepinex", "plugins", "Some-Mod", "A.dll")); got != "new" {
		t.Fatalf("the update did not happen: %q", got)
	}

	if err := undo(context.Background()); err != nil {
		t.Fatalf("undo() error = %v", err)
	}
	if got := read(t, filepath.Join(dir, "bepinex", "plugins", "Some-Mod", "A.dll")); got != "old" {
		t.Errorf("after undo the file is %q, want the version that was there before", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "bepinex", "plugins", "Other-Mod")); err == nil {
		t.Error("after undo the newly installed mod is still there")
	}
	if v := second.Installed("Some-Mod").Version; v != "1.0.0" {
		t.Errorf("after undo the manifest says %q, want the version that is on disk", v)
	}
}

// Reinstalling megabytes on every apply is how an apply stops being something
// people run.
func TestSyncSkipsWhatIsAlreadyCurrent(t *testing.T) {
	dir := t.TempDir()
	var downloads int
	zipped := pkgZip(t, map[string]string{"plugins/A.dll": "a"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads++
		w.Write(zipped)
	}))
	t.Cleanup(srv.Close)

	install := mods.Install{
		Root:   dir,
		Layout: layout,
		Source: releases{"Some-Mod": {ID: "Some-Mod", Version: "1.0.0", URL: srv.URL + "/a.zip"}},
	}
	refs := []model.ModRef{{ID: "Some-Mod"}}
	for i := 0; i < 3; i++ {
		if _, err := install.Sync(context.Background(), refs, nil); err != nil {
			t.Fatalf("Sync() error = %v", err)
		}
	}
	if downloads != 1 {
		t.Errorf("downloaded %d times, want once", downloads)
	}
}

// The loader is the image's job. Installing a second copy underneath it is
// how you get two doorstops pointing at the same assembly.
func TestSyncSkipsWhatTheImageInstalls(t *testing.T) {
	dir := t.TempDir()
	install := mods.Install{
		Root:   dir,
		Layout: layout,
		Source: releases{},
		Skip:   []string{"denikson-BepInExPack_Valheim"},
	}
	if _, err := install.Sync(context.Background(), []model.ModRef{{ID: "denikson-BepInExPack_Valheim"}}, nil); err != nil {
		t.Fatalf("Sync() error = %v, want the bundled pack skipped rather than resolved", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bepinex", "plugins", "denikson-BepInExPack_Valheim")); err == nil {
		t.Error("the bundled loader was installed")
	}
}

// A package is a zip somebody else made, and a zip can name ../../anything.
func TestAPackageCannotEscapeItsDirectory(t *testing.T) {
	dir := t.TempDir()
	zipped := pkgZip(t, map[string]string{"plugins/../../../escaped.dll": "no"})
	srv := zipServer(t, map[string][]byte{"/evil.zip": zipped})

	install := mods.Install{
		Root:   dir,
		Layout: layout,
		Source: releases{"Bad-Mod": {ID: "Bad-Mod", Version: "1.0.0", URL: srv.URL + "/evil.zip"}},
	}
	_, err := install.Sync(context.Background(), []model.ModRef{{ID: "Bad-Mod"}}, nil)
	if err == nil {
		t.Fatal("Sync() accepted a package that writes outside the mod directory")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Errorf("error = %v, want it to say the package escaped its directory", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escaped.dll")); err == nil {
		t.Error("the package wrote outside the mod directory")
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// HookGenPatcher ships patchers and nothing else. BepInEx reads those before
// the game's assemblies exist and never looks for them among the plugins, so
// installing one as a plugin is installing a file nothing will ever read —
// which looks exactly like a working install until a mod that needs it fails.
func TestPatchersGoWhereThePatchersGo(t *testing.T) {
	dir := t.TempDir()
	zipped := pkgZip(t, map[string]string{
		"patchers/HookGenPatcher/HookGenPatcher.dll": "patcher",
		"config/HookGenPatcher.cfg":                  "the author's defaults",
		"manifest.json":                              "{}",
	})
	srv := zipServer(t, map[string][]byte{"/hook.zip": zipped})

	install := mods.Install{
		Root:   dir,
		Layout: layout,
		Source: releases{"ValheimModding-HookGenPatcher": {ID: "ValheimModding-HookGenPatcher", Version: "0.0.4", URL: srv.URL + "/hook.zip"}},
	}
	if _, err := install.Sync(context.Background(), []model.ModRef{{ID: "ValheimModding-HookGenPatcher"}}, nil); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	landed := filepath.Join(dir, "bepinex", "patchers", "ValheimModding-HookGenPatcher", "HookGenPatcher", "HookGenPatcher.dll")
	if got := read(t, landed); got != "patcher" {
		t.Errorf("the patcher did not land in the patchers directory: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "bepinex", "plugins", "ValheimModding-HookGenPatcher")); err == nil {
		t.Error("a patchers-only package also created a plugins directory")
	}
	// The config goes to the config directory, not into the mod's own — and
	// for this package it is the whole point: without it HookGenPatcher
	// hooks the wrong assembly and every mod that needs the hooks fails.
	if got := read(t, filepath.Join(dir, "bepinex", "HookGenPatcher.cfg")); got != "the author's defaults" {
		t.Errorf("the shipped config did not land: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "bepinex", "patchers", "ValheimModding-HookGenPatcher", "config")); err == nil {
		t.Error("the config was installed inside the mod's directory too")
	}
}

// A package with both kinds occupies two directories, and pruning has to know
// about both or it leaves half a mod behind.
func TestAPackageWithBothKindsIsTrackedInBoth(t *testing.T) {
	dir := t.TempDir()
	zipped := pkgZip(t, map[string]string{
		"plugins/Thing.dll":       "plugin",
		"patchers/ThingPatch.dll": "patcher",
	})
	srv := zipServer(t, map[string][]byte{"/both.zip": zipped})

	install := mods.Install{
		Root:   dir,
		Layout: layout,
		Source: releases{"Some-Both": {ID: "Some-Both", Version: "1.0.0", URL: srv.URL + "/both.zip"}},
	}
	refs := []model.ModRef{{ID: "Some-Both"}}
	if _, err := install.Sync(context.Background(), refs, nil); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	plugin := filepath.Join(dir, "bepinex", "plugins", "Some-Both")
	patcher := filepath.Join(dir, "bepinex", "patchers", "Some-Both")
	for _, d := range []string{plugin, patcher} {
		if _, err := os.Stat(d); err != nil {
			t.Fatalf("%s is missing: %v", d, err)
		}
	}

	// Dropped from the configuration: both halves go.
	if _, err := install.Sync(context.Background(), nil, nil); err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	for _, d := range []string{plugin, patcher} {
		if _, err := os.Stat(d); err == nil {
			t.Errorf("%s survived the prune, so half the mod is still loaded", d)
		}
	}
}

// A game whose loader has no patchers cannot install a package that is only
// patchers, and saying so beats writing the files somewhere nothing reads.
func TestAPatcherWithNowhereToGoIsRefused(t *testing.T) {
	dir := t.TempDir()
	zipped := pkgZip(t, map[string]string{"patchers/X.dll": "patcher"})
	srv := zipServer(t, map[string][]byte{"/p.zip": zipped})

	install := mods.Install{
		Root:   dir,
		Layout: mods.Layout{Plugins: "mods"}, // no patchers
		Source: releases{"Some-Patcher": {ID: "Some-Patcher", Version: "1.0.0", URL: srv.URL + "/p.zip"}},
	}
	_, err := install.Sync(context.Background(), []model.ModRef{{ID: "Some-Patcher"}}, nil)
	if err == nil {
		t.Fatal("Sync() accepted a package this game has nowhere to put")
	}
	if !strings.Contains(err.Error(), "nowhere") {
		t.Errorf("error = %v, want it to say why", err)
	}
}

// The rule that keeps both halves true: a package's defaults arrive when
// nothing is there, and an operator's edits are never overwritten by an
// update. Both mod managers work this way, and so does the server image.
func TestAPackageConfigNeverOverwritesTheOperators(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bepinex"), 0o755); err != nil {
		t.Fatal(err)
	}
	tuned := filepath.Join(dir, "bepinex", "Mine.cfg")
	if err := os.WriteFile(tuned, []byte("an afternoon of tuning"), 0o644); err != nil {
		t.Fatal(err)
	}

	zipped := pkgZip(t, map[string]string{
		"plugins/Thing.dll": "plugin",
		"config/Mine.cfg":   "the author's defaults",
		"config/New.cfg":    "also the author's",
	})
	srv := zipServer(t, map[string][]byte{"/c.zip": zipped})
	install := mods.Install{
		Root:   dir,
		Layout: layout,
		Source: releases{"Some-Mod": {ID: "Some-Mod", Version: "1.0.0", URL: srv.URL + "/c.zip"}},
	}
	if _, err := install.Sync(context.Background(), []model.ModRef{{ID: "Some-Mod"}}, nil); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	if got := read(t, tuned); got != "an afternoon of tuning" {
		t.Errorf("the operator's config was overwritten: %q", got)
	}
	if got := read(t, filepath.Join(dir, "bepinex", "New.cfg")); got != "also the author's" {
		t.Errorf("a config with nothing in its way did not land: %q", got)
	}
}

// Removing a mod takes its assemblies and leaves its settings. Configuration
// is the operator's work, and a reinstall a week later should find it.
func TestPruningLeavesConfigurationAlone(t *testing.T) {
	dir := t.TempDir()
	zipped := pkgZip(t, map[string]string{
		"plugins/Thing.dll": "plugin",
		"config/Thing.cfg":  "settings",
	})
	srv := zipServer(t, map[string][]byte{"/c.zip": zipped})
	install := mods.Install{
		Root:   dir,
		Layout: layout,
		Source: releases{"Some-Mod": {ID: "Some-Mod", Version: "1.0.0", URL: srv.URL + "/c.zip"}},
	}
	if _, err := install.Sync(context.Background(), []model.ModRef{{ID: "Some-Mod"}}, nil); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if _, err := install.Sync(context.Background(), nil, nil); err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "bepinex", "plugins", "Some-Mod")); err == nil {
		t.Error("the mod was not removed")
	}
	if got := read(t, filepath.Join(dir, "bepinex", "Thing.cfg")); got != "settings" {
		t.Errorf("the configuration went with it: %q", got)
	}
}
