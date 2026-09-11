package mods_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/services/mods"
)

// thunderstoreServer answers the per-package endpoint from the captured
// fixture, and 404s for anything else — which is what a mistyped id gets.
func thunderstoreServer(t *testing.T) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile("testdata/jotunn.json")
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/package/ValheimModding/Jotunn/" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Accept"); !strings.Contains(got, "json") {
			t.Errorf("Accept = %q, want JSON", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The fixture is a real answer from thunderstore.io, so this is a test of the
// shape the service actually meets rather than of a mock written to match the
// parser.
func TestThunderstoreResolvesAPackage(t *testing.T) {
	srv := thunderstoreServer(t)
	ts := mods.Thunderstore{Endpoint: srv.URL + "/package/"}

	out, err := ts.Resolve(context.Background(), []model.ModRef{{ID: "ValheimModding-Jotunn"}})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d mods, want 1", len(out))
	}
	got := out[0]
	if got.Name != "Jotunn" {
		t.Errorf("Name = %q, want Jotunn", got.Name)
	}
	if got.Available == "" {
		t.Error("Available is empty, so no update could ever be detected")
	}
	if len(got.Requires) == 0 {
		t.Error("Requires is empty, but the fixture declares a dependency")
	}
	if got.Err != "" {
		t.Errorf("Err = %q, want none", got.Err)
	}
}

// The URL form is what a person copies out of the address bar, so it resolves
// to the same package as the full name.
func TestThunderstoreAcceptsTheURLForm(t *testing.T) {
	srv := thunderstoreServer(t)
	ts := mods.Thunderstore{Endpoint: srv.URL + "/package/"}

	out, err := ts.Resolve(context.Background(), []model.ModRef{{ID: "ValheimModding/Jotunn"}})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if out[0].Name != "Jotunn" {
		t.Errorf("Name = %q, want Jotunn", out[0].Name)
	}
}

// A typo is the usual cause, so the row keeps the id and says what happened
// rather than vanishing from a list the operator wrote.
func TestThunderstoreReportsAnUnknownPackage(t *testing.T) {
	srv := thunderstoreServer(t)
	ts := mods.Thunderstore{Endpoint: srv.URL + "/package/"}

	out, err := ts.Resolve(context.Background(), []model.ModRef{{ID: "Someone-Nothing"}})
	if err != nil {
		t.Fatalf("Resolve() error = %v, want the failure on the row", err)
	}
	if out[0].ID != "Someone-Nothing" {
		t.Errorf("ID = %q, want the configured id kept", out[0].ID)
	}
	if !strings.Contains(out[0].Err, "Someone-Nothing") {
		t.Errorf("Err = %q, want it to name the package", out[0].Err)
	}
}

// An update badge needs two versions. The source knows one of them; only the
// installer knows what is actually on disk.
func TestThunderstoreReportsTheInstalledVersion(t *testing.T) {
	srv := thunderstoreServer(t)
	ts := mods.Thunderstore{
		Endpoint:  srv.URL + "/package/",
		Installed: func(string) mods.InstalledMod { return mods.InstalledMod{Version: "2.0.0", Bytes: 42} },
	}

	out, _ := ts.Resolve(context.Background(), []model.ModRef{{ID: "ValheimModding-Jotunn"}})
	if out[0].Version != "2.0.0" {
		t.Errorf("Version = %q, want what is installed", out[0].Version)
	}
	if !out[0].NeedsUpdate() {
		t.Error("NeedsUpdate() = false, but the source knows a newer version")
	}
}

// A pin is the operator naming a version. Thunderstore's download URLs are
// positional, so honouring one costs no request at all.
func TestAPinnedModIsNotLookedUp(t *testing.T) {
	var asked int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	ts := mods.Thunderstore{Endpoint: srv.URL + "/package/"}
	rel, err := ts.Release(context.Background(), model.ModRef{ID: "ValheimModding-Jotunn", Pin: "2.11.0"})
	if err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if asked != 0 {
		t.Errorf("asked Thunderstore %d times for a pinned version, want 0", asked)
	}
	if rel.Version != "2.11.0" {
		t.Errorf("Version = %q, want the pin", rel.Version)
	}
	if !strings.HasSuffix(rel.URL, "/ValheimModding/Jotunn/2.11.0/") {
		t.Errorf("URL = %q, want the pinned version's download", rel.URL)
	}
}

func TestAnIdThatIsNotAPackageIsRefused(t *testing.T) {
	ts := mods.Thunderstore{}
	if _, err := ts.Release(context.Background(), model.ModRef{ID: "Jotunn"}); err == nil {
		t.Error("Release() accepted an id with no owner")
	}
}
