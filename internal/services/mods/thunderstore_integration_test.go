//go:build integration

package mods_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/services/mods"
)

// The fixture pins the shape of Thunderstore's answer; this pins that the
// shape is still what Thunderstore sends. It needs the network, which is why
// it is behind the build tag — but a resolver whose only evidence is a
// captured file is a resolver that keeps working after the service changes.
//
//	go test -tags integration ./internal/services/mods/
func TestAgainstTheRealThunderstore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	ref := model.ModRef{ID: "ValheimModding-Jotunn"}
	ts := mods.Thunderstore{}

	found, err := ts.Resolve(ctx, []model.ModRef{ref})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if found[0].Err != "" {
		t.Fatalf("resolving a package that exists: %s", found[0].Err)
	}
	if found[0].Name == "" || found[0].Available == "" {
		t.Fatalf("resolved to %+v, want a name and a version", found[0])
	}

	dir := t.TempDir()
	install := mods.Install{Dir: dir, Source: ts}
	if _, err := install.Sync(ctx, []model.ModRef{ref}, func(s string) { t.Log(s) }); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}

	// The package's own layout is the thing being tested: plugins/ content
	// lands in the mod's directory and the packaging files do not.
	dll := filepath.Join(dir, "ValheimModding-Jotunn", "Jotunn.dll")
	if _, err := os.Stat(dll); err != nil {
		entries, _ := os.ReadDir(filepath.Join(dir, "ValheimModding-Jotunn"))
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("no Jotunn.dll installed; the directory holds %v", names)
	}
	if _, err := os.Stat(filepath.Join(dir, "ValheimModding-Jotunn", "manifest.json")); err == nil {
		t.Error("Thunderstore's packaging was installed as if it were plugin content")
	}
	if got := install.Installed("ValheimModding-Jotunn"); got.Version != found[0].Available {
		t.Errorf("installed %q, want the version that was resolved (%q)", got.Version, found[0].Available)
	}
}
