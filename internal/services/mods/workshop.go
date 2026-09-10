// Package mods resolves what a server's configured mods actually are.
//
// A server's TOML holds ids and pins — "2822286426", maybe "2.11.0" — which is
// all the game needs and nothing a person can read. Resolving turns that into
// a name, a version, and whether something newer exists, which is what makes
// the Mods screen a list rather than a column of numbers.
//
// The network is the reason this is a service and not part of the plugin. A
// plugin's methods are pure and called on every render; asking Steam about a
// mod is neither.
package mods

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/camden-brown/garrison/internal/model"
)

// DefaultInterval is how often mods are re-resolved.
//
// Hourly, which is DESIGN's figure and generous on purpose: a Workshop item
// changes a few times a year, the answer is only used to draw a badge, and
// Steam's API is somebody else's service being asked a favour.
const DefaultInterval = time.Hour

// workshopAPI is Steam's published details endpoint. It takes form-encoded
// ids and needs no key, which is why this works without asking the operator
// for one.
const workshopAPI = "https://api.steampowered.com/ISteamRemoteStorage/GetPublishedFileDetails/v1/"

// Client is the HTTP client, overridable so the tests drive a real JSON
// exchange against an in-process server instead of the internet.
type Client interface {
	Do(req *http.Request) (*http.Response, error)
}

// Workshop resolves Steam Workshop items.
type Workshop struct {
	// HTTP is the client. Nil means a default with a timeout, because a
	// resolver that hangs stops the poller that drives it.
	HTTP Client
	// Endpoint is overridable for the tests.
	Endpoint string
}

// Resolve looks up every ref in one request.
//
// One request rather than one per mod: the endpoint takes a list, a server can
// easily have forty mods, and forty round trips to draw a badge is the kind of
// thing that gets a fleet rate-limited.
//
// Order is preserved. For a game where load order matters the sequence is the
// operator's decision, and a resolver that returned them in the API's order
// would silently reorder their server on the next apply.
func (w Workshop) Resolve(ctx context.Context, refs []model.ModRef) ([]model.Mod, error) {
	out := make([]model.Mod, 0, len(refs))
	for _, r := range refs {
		out = append(out, model.Mod{ID: r.ID, Pin: r.Pin, Enabled: true})
	}
	if len(refs) == 0 {
		return out, nil
	}

	form := url.Values{}
	form.Set("itemcount", strconv.Itoa(len(refs)))
	for i, r := range refs {
		form.Set(fmt.Sprintf("publishedfileids[%d]", i), r.ID)
	}

	endpoint := w.Endpoint
	if endpoint == "" {
		endpoint = workshopAPI
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := w.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}

	resp, err := client.Do(req)
	if err != nil {
		return out, fmt.Errorf("asking the Workshop about %d mods: %w", len(refs), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("the Workshop answered %s", resp.Status)
	}

	var body struct {
		Response struct {
			PublishedFileDetails []struct {
				PublishedFileID string `json:"publishedfileid"`
				Result          int    `json:"result"`
				Title           string `json:"title"`
				FileSize        any    `json:"file_size"`
				TimeUpdated     int64  `json:"time_updated"`
			} `json:"publishedfiledetails"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return out, fmt.Errorf("reading the Workshop's answer: %w", err)
	}

	byID := map[string]int{}
	for i, m := range out {
		byID[m.ID] = i
	}

	for _, d := range body.Response.PublishedFileDetails {
		i, ok := byID[d.PublishedFileID]
		if !ok {
			continue
		}
		// result 1 is success; anything else is an item that is hidden,
		// deleted, or never existed. Saying so beats a blank row, because
		// the operator wrote that id themselves and the answer is usually
		// a typo.
		if d.Result != 1 {
			out[i].Err = "the Workshop has no visible item with this id"
			continue
		}

		out[i].Name = d.Title
		out[i].SizeBytes = parseSize(d.FileSize)

		// The Workshop has no version numbers. What it has is a last-updated
		// timestamp, which is the only thing an update check can compare —
		// so that is what a "version" means here, and it is dated rather
		// than pretending to be semantic.
		if d.TimeUpdated > 0 {
			out[i].Available = time.Unix(d.TimeUpdated, 0).UTC().Format("2006-01-02")
		}
	}
	return out, nil
}

// parseSize copes with file_size arriving as a number or a string, which the
// endpoint has done both of.
func parseSize(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case string:
		out, _ := strconv.ParseInt(n, 10, 64)
		return out
	}
	return 0
}

// Refs is where the mods to resolve come from, keyed by server.
//
// Declared here rather than taken as a store, because a service never imports
// core: cmd knows both and wires them (ADR 0007).
type Refs interface {
	ModRefs() map[string][]model.ModRef
}

// Resolver is what a source does. Workshop implements it; a game with a
// different source would supply its own.
type Resolver interface {
	Resolve(ctx context.Context, refs []model.ModRef) ([]model.Mod, error)
}

// Sources picks the resolver for a server, or nil for a game with no mod
// system. cmd implements it by asserting games.Moddable and reading
// ModSource.
type Sources interface {
	For(server string) Resolver
}

// Observer receives the resolved list.
type Observer interface {
	ModsResolved(ctx context.Context, at time.Time, server string, mods []model.Mod)
}

// Poller re-resolves every server's mods on an interval.
type Poller struct {
	Interval time.Duration
	Refs     Refs
	Sources  Sources
}

// Run polls until the context is cancelled.
func (p *Poller) Run(ctx context.Context, obs Observer) {
	if p.Refs == nil || p.Sources == nil || obs == nil {
		return
	}
	interval := p.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}

	p.once(ctx, obs)

	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			p.once(ctx, obs)
		}
	}
}

func (p *Poller) once(ctx context.Context, obs Observer) {
	for server, refs := range p.Refs.ModRefs() {
		resolver := p.Sources.For(server)
		if resolver == nil {
			// A game with no mod system, or one whose source Garrison has
			// no resolver for. The Mods screen already explains that; there
			// is nothing to report here.
			continue
		}

		mods, err := resolver.Resolve(ctx, refs)
		if err != nil {
			// Report what came back anyway. Resolve fills in the ids and
			// pins before it asks anybody, so a failed lookup still leaves
			// the list the operator configured — which is more useful than
			// an empty screen and is exactly what they would see before the
			// first poll.
			obs.ModsResolved(ctx, time.Now(), server, mods)
			continue
		}
		obs.ModsResolved(ctx, time.Now(), server, mods)
	}
}
