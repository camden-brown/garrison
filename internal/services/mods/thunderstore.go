package mods

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/camden-brown/garrison/internal/model"
)

// thunderstoreAPI is the per-package endpoint. It needs no key and answers
// with the latest version and its download URL, which is everything an update
// badge and an install both need.
const thunderstoreAPI = "https://thunderstore.io/api/experimental/package/"

// Thunderstore resolves packages from thunderstore.io, which is where Valheim
// mods are published — the game has no Steam Workshop at all.
//
// Unlike the Workshop there is no endpoint that takes a list, so this is one
// request per mod. That is affordable because the poller runs hourly and a
// server has tens of mods, not thousands; it is also why Resolve keeps going
// after a failure rather than abandoning the whole list.
type Thunderstore struct {
	// HTTP is the client. Nil means a default with a timeout.
	HTTP Client
	// Endpoint is overridable for the tests.
	Endpoint string
	// Installed reports what version of a mod is on disk, or empty when
	// nothing is. Without it Resolve can say what the newest version is and
	// not whether the server is behind it — an update badge needs both, and
	// only the installer knows the first half.
	Installed func(id string) InstalledMod
}

// InstalledMod is what the installer recorded about a mod it put in place.
type InstalledMod struct {
	Version string
	Bytes   int64
}

// Resolve looks each ref up by its Thunderstore id.
//
// Ids are the site's own full names — "ValheimModding-Jotunn" — because that
// is what a person copies out of a package page and what the dependency lists
// in the API use. "ValheimModding/Jotunn" is accepted too, since that is what
// the URL looks like.
func (t Thunderstore) Resolve(ctx context.Context, refs []model.ModRef) ([]model.Mod, error) {
	out := make([]model.Mod, 0, len(refs))
	for _, r := range refs {
		out = append(out, model.Mod{ID: r.ID, Pin: r.Pin, Enabled: true})
	}

	for i, r := range refs {
		if on := t.installed(r.ID); on.Version != "" {
			out[i].Version = on.Version
			out[i].SizeBytes = on.Bytes
		}

		pkg, err := t.fetch(ctx, r.ID)
		if err != nil {
			// One mod that could not be looked up is not a failed poll.
			// The row keeps the id the operator wrote and says why it is
			// bare, which is the thing they need to see.
			out[i].Err = err.Error()
			continue
		}

		out[i].Name = pkg.Name
		out[i].Available = pkg.Latest.Version
		out[i].Requires = pkg.Latest.Dependencies
		if pkg.Deprecated {
			out[i].Err = "the author has marked this package deprecated"
		}
	}
	return out, nil
}

func (t Thunderstore) installed(id string) InstalledMod {
	if t.Installed == nil {
		return InstalledMod{}
	}
	return t.Installed(id)
}

// Release is one version of a package: what to fetch and what to record as
// installed once it is unpacked.
type Release struct {
	ID      string
	Version string
	URL     string
}

// Release resolves what a ref asks to have installed: its pin if it has one,
// otherwise whatever is newest.
//
// A pinned version is not looked up. Thunderstore's download URLs are
// positional — /package/download/<owner>/<name>/<version>/ — so a pin can be
// fetched without asking, and asking would only report the newest version we
// are deliberately not installing.
func (t Thunderstore) Release(ctx context.Context, ref model.ModRef) (Release, error) {
	namespace, name, err := splitID(ref.ID)
	if err != nil {
		return Release{}, err
	}
	full := namespace + "-" + name

	if ref.Pin != "" {
		return Release{
			ID:      full,
			Version: ref.Pin,
			URL: fmt.Sprintf("https://thunderstore.io/package/download/%s/%s/%s/",
				namespace, name, ref.Pin),
		}, nil
	}

	found, err := t.fetch(ctx, ref.ID)
	if err != nil {
		return Release{}, err
	}
	if found.Latest.Version == "" || found.Latest.DownloadURL == "" {
		return Release{}, fmt.Errorf("Thunderstore has no downloadable version of %s", ref.ID)
	}
	return Release{ID: full, Version: found.Latest.Version, URL: found.Latest.DownloadURL}, nil
}

// pkg is the half of the API response Garrison uses.
type pkg struct {
	Name       string
	Deprecated bool
	Latest     release
}

// release is one published version of a package.
type release struct {
	Version      string
	DownloadURL  string
	Dependencies []string
}

// fetch asks about one package.
func (t Thunderstore) fetch(ctx context.Context, id string) (pkg, error) {
	namespace, name, err := splitID(id)
	if err != nil {
		return pkg{}, err
	}

	endpoint := t.Endpoint
	if endpoint == "" {
		endpoint = thunderstoreAPI
	}
	url := strings.TrimSuffix(endpoint, "/") + "/" + namespace + "/" + name + "/"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return pkg{}, err
	}
	req.Header.Set("Accept", "application/json")

	client := t.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return pkg{}, fmt.Errorf("asking Thunderstore about %s: %w", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// The usual cause is a typo in an id somebody typed by hand, so say
		// the thing that points at the typo.
		return pkg{}, fmt.Errorf("Thunderstore has no package called %q", id)
	}
	if resp.StatusCode != http.StatusOK {
		return pkg{}, fmt.Errorf("Thunderstore answered %s for %s", resp.Status, id)
	}

	var body struct {
		Name         string `json:"name"`
		FullName     string `json:"full_name"`
		IsDeprecated bool   `json:"is_deprecated"`
		Latest       struct {
			VersionNumber string   `json:"version_number"`
			DownloadURL   string   `json:"download_url"`
			Dependencies  []string `json:"dependencies"`
		} `json:"latest"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return pkg{}, fmt.Errorf("reading Thunderstore's answer for %s: %w", id, err)
	}

	return pkg{
		Name:       body.Name,
		Deprecated: body.IsDeprecated,
		Latest: release{
			Version:      body.Latest.VersionNumber,
			DownloadURL:  body.Latest.DownloadURL,
			Dependencies: body.Latest.Dependencies,
		},
	}, nil
}

// splitID turns a package id into its two halves.
//
// Thunderstore namespaces and names are letters, digits and underscores only,
// so the single hyphen in a full name is unambiguous — which is what makes
// "Owner-Package" usable as an id at all.
func splitID(id string) (namespace, name string, err error) {
	id = strings.TrimSpace(id)
	sep := strings.IndexAny(id, "-/")
	if sep <= 0 || sep == len(id)-1 {
		return "", "", fmt.Errorf("%q is not a Thunderstore package id: it should look like Owner-Package", id)
	}
	namespace, name = id[:sep], id[sep+1:]
	if strings.ContainsAny(name, "/") {
		return "", "", fmt.Errorf("%q is not a Thunderstore package id: it should look like Owner-Package", id)
	}
	return namespace, name, nil
}
