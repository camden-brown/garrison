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
	dir := filepath.Join(t.TempDir(), "bepinex", "plugins")
	zipped := pkgZip(t, map[string]string{
		"plugins/Jotunn.dll":       "assembly",
		"plugins/sub/asset.bundle": "asset",
		"manifest.json":            "{}",
		"README.md":                "read me",
		"BepInEx/config/x.cfg":     "do not overwrite the operator's config",
	})
	srv := zipServer(t, map[string][]byte{"/jotunn.zip": zipped})

	install := mods.Install{
		Dir:    dir,
		Source: releases{"ValheimModding-Jotunn": {ID: "ValheimModding-Jotunn", Version: "2.30.0", URL: srv.URL + "/jotunn.zip"}},
	}

	if _, err := install.Sync(context.Background(), []model.ModRef{{ID: "ValheimModding-Jotunn"}}, nil); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	// One directory per mod, which is what makes pruning exact and what
	// BepInEx walks anyway.
	if got := read(t, filepath.Join(dir, "ValheimModding-Jotunn", "Jotunn.dll")); got != "assembly" {
		t.Errorf("the plugin did not land: %q", got)
	}
	if got := read(t, filepath.Join(dir, "ValheimModding-Jotunn", "sub", "asset.bundle")); got != "asset" {
		t.Errorf("a nested file did not land: %q", got)
	}
	for _, unwanted := range []string{"manifest.json", "README.md", filepath.Join("BepInEx", "config", "x.cfg")} {
		if _, err := os.Stat(filepath.Join(dir, "ValheimModding-Jotunn", unwanted)); err == nil {
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
		Dir:    dir,
		Source: releases{"Some-Mod": {ID: "Some-Mod", Version: "1.0.0", URL: srv.URL + "/a.zip"}},
	}

	if _, err := install.Sync(context.Background(), []model.ModRef{{ID: "Some-Mod"}}, nil); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	// And again with nothing configured, which prunes.
	if _, err := install.Sync(context.Background(), nil, nil); err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "Some-Mod")); err == nil {
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
		Dir:    dir,
		Source: releases{"Some-Mod": {ID: "Some-Mod", Version: "1.0.0", URL: srv.URL + "/old.zip"}},
	}
	if _, err := first.Sync(context.Background(), []model.ModRef{{ID: "Some-Mod"}}, nil); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	second := mods.Install{
		Dir: dir,
		Source: releases{
			"Some-Mod":  {ID: "Some-Mod", Version: "2.0.0", URL: srv.URL + "/new.zip"},
			"Other-Mod": {ID: "Other-Mod", Version: "1.0.0", URL: srv.URL + "/new.zip"},
		},
	}
	undo, err := second.Sync(context.Background(), []model.ModRef{{ID: "Some-Mod"}, {ID: "Other-Mod"}}, nil)
	if err != nil {
		t.Fatalf("second Sync() error = %v", err)
	}
	if got := read(t, filepath.Join(dir, "Some-Mod", "A.dll")); got != "new" {
		t.Fatalf("the update did not happen: %q", got)
	}

	if err := undo(context.Background()); err != nil {
		t.Fatalf("undo() error = %v", err)
	}
	if got := read(t, filepath.Join(dir, "Some-Mod", "A.dll")); got != "old" {
		t.Errorf("after undo the file is %q, want the version that was there before", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "Other-Mod")); err == nil {
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
		Dir:    dir,
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
		Dir:    dir,
		Source: releases{},
		Skip:   []string{"denikson-BepInExPack_Valheim"},
	}
	if _, err := install.Sync(context.Background(), []model.ModRef{{ID: "denikson-BepInExPack_Valheim"}}, nil); err != nil {
		t.Fatalf("Sync() error = %v, want the bundled pack skipped rather than resolved", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "denikson-BepInExPack_Valheim")); err == nil {
		t.Error("the bundled loader was installed")
	}
}

// A package is a zip somebody else made, and a zip can name ../../anything.
func TestAPackageCannotEscapeItsDirectory(t *testing.T) {
	dir := t.TempDir()
	zipped := pkgZip(t, map[string]string{"plugins/../../../escaped.dll": "no"})
	srv := zipServer(t, map[string][]byte{"/evil.zip": zipped})

	install := mods.Install{
		Dir:    dir,
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
