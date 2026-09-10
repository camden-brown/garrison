package mods_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/services/mods"
)

// A real HTTP server speaking the endpoint's own JSON, rather than a mock of
// the client's calls: the form encoding and the result code are the parts
// worth being wrong about.
func workshopServer(t *testing.T, handler func(ids []string) any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("the request was not form-encoded: %v", err)
		}
		var ids []string
		for i := 0; ; i++ {
			v := r.PostForm.Get("publishedfileids[" + itoa(i) + "]")
			if v == "" {
				break
			}
			ids = append(ids, v)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(handler(ids))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func detail(id, title string, updated int64, result int) map[string]any {
	return map[string]any{
		"publishedfileid": id, "title": title,
		"time_updated": updated, "result": result, "file_size": "1048576",
	}
}

func reply(details ...map[string]any) any {
	return map[string]any{"response": map[string]any{"publishedfiledetails": details}}
}

func TestResolveNamesTheMods(t *testing.T) {
	srv := workshopServer(t, func(ids []string) any {
		if len(ids) != 2 {
			t.Errorf("asked about %d ids, want 2 in one request", len(ids))
		}
		return reply(
			detail("2822286426", "Hydrocraft", 1756000000, 1),
			detail("1299328280", "Brita's Weapons", 1750000000, 1),
		)
	})

	w := mods.Workshop{Endpoint: srv.URL}
	got, err := w.Resolve(context.Background(), []model.ModRef{
		{ID: "2822286426"}, {ID: "1299328280"},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d mods, want 2", len(got))
	}
	if got[0].Name != "Hydrocraft" {
		t.Errorf("first mod is %+v, want Hydrocraft", got[0])
	}
	if got[0].SizeBytes != 1<<20 {
		t.Errorf("size = %d, want 1 MiB", got[0].SizeBytes)
	}
	if got[0].Available == "" {
		t.Error("no available version, so no update check is possible")
	}
}

// Order is the operator's decision for a game where load order matters. A
// resolver returning them in the API's order would silently reorder their
// server on the next apply.
func TestResolvePreservesTheConfiguredOrder(t *testing.T) {
	srv := workshopServer(t, func(ids []string) any {
		// Answer in the opposite order on purpose.
		return reply(
			detail("second", "Second", 1750000000, 1),
			detail("first", "First", 1750000000, 1),
		)
	})

	w := mods.Workshop{Endpoint: srv.URL}
	got, _ := w.Resolve(context.Background(), []model.ModRef{{ID: "first"}, {ID: "second"}})

	if len(got) != 2 || got[0].ID != "first" || got[1].ID != "second" {
		t.Errorf("order = %v, want the configured one", []string{got[0].ID, got[1].ID})
	}
}

// The operator wrote that id themselves and the answer is usually a typo, so
// a missing item is a problem to show rather than a row to omit.
func TestResolveReportsAnUnknownItem(t *testing.T) {
	srv := workshopServer(t, func(ids []string) any {
		return reply(detail("9999999999", "", 0, 9))
	})

	w := mods.Workshop{Endpoint: srv.URL}
	got, err := w.Resolve(context.Background(), []model.ModRef{{ID: "9999999999"}})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d mods", len(got))
	}
	if got[0].Err == "" {
		t.Error("an unknown item was reported as fine")
	}
}

// A failed lookup still leaves the list the operator configured, which is
// what they would see before the first poll anyway.
func TestResolveKeepsTheConfiguredListOnFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	w := mods.Workshop{Endpoint: srv.URL}
	got, err := w.Resolve(context.Background(), []model.ModRef{{ID: "2822286426", Pin: "1.0"}})
	if err == nil {
		t.Error("a 500 was reported as success")
	}
	if len(got) != 1 || got[0].ID != "2822286426" || got[0].Pin != "1.0" {
		t.Errorf("got %+v, want the configured ref preserved", got)
	}
}

func TestResolveWithNoModsAsksNobody(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	defer srv.Close()

	w := mods.Workshop{Endpoint: srv.URL}
	got, err := w.Resolve(context.Background(), nil)
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v", got, err)
	}
	if called {
		t.Error("a server with no mods still made a request")
	}
}

// A pin is the operator saying which version they want, so a badge nagging
// about a deliberate choice is noise.
func TestAPinnedModNeverNeedsAnUpdate(t *testing.T) {
	pinned := model.Mod{Pin: "2.11.0", Version: "2.11.0", Available: "2026-09-01"}
	if pinned.NeedsUpdate() {
		t.Error("a pinned mod was reported as needing an update")
	}

	tracking := model.Mod{Version: "2026-08-01", Available: "2026-09-01"}
	if !tracking.NeedsUpdate() {
		t.Error("an out-of-date unpinned mod was reported as current")
	}

	unknown := model.Mod{Version: "2026-09-01"}
	if unknown.NeedsUpdate() {
		t.Error("a mod nobody has looked up was reported as needing an update")
	}
}

// ---- the poller ---------------------------------------------------------

type stubRefs struct{ refs map[string][]model.ModRef }

func (s stubRefs) ModRefs() map[string][]model.ModRef { return s.refs }

type stubSources struct{ resolver mods.Resolver }

func (s stubSources) For(string) mods.Resolver { return s.resolver }

type stubResolver struct {
	mu    sync.Mutex
	calls int
	out   []model.Mod
}

func (s *stubResolver) Resolve(context.Context, []model.ModRef) ([]model.Mod, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.out, nil
}

type collector struct {
	mu   sync.Mutex
	last map[string][]model.Mod
	got  chan struct{}
}

func (c *collector) ModsResolved(_ context.Context, _ time.Time, server string, m []model.Mod) {
	c.mu.Lock()
	c.last[server] = m
	c.mu.Unlock()
	select {
	case c.got <- struct{}{}:
	default:
	}
}

func TestPollerResolvesAndReports(t *testing.T) {
	c := &collector{last: map[string][]model.Mod{}, got: make(chan struct{}, 8)}
	res := &stubResolver{out: []model.Mod{{ID: "1", Name: "One"}}}

	ctx, cancel := context.WithCancel(context.Background())
	go (&mods.Poller{
		Interval: time.Hour,
		Refs:     stubRefs{refs: map[string][]model.ModRef{"zomboid-main": {{ID: "1"}}}},
		Sources:  stubSources{resolver: res},
	}).Run(ctx, c)

	select {
	case <-c.got:
	case <-time.After(2 * time.Second):
		t.Fatal("the poller never reported")
	}
	cancel()

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.last["zomboid-main"]) != 1 {
		t.Errorf("reported %v", c.last["zomboid-main"])
	}
}

// A game with no mod system has nothing to report, and the Mods screen
// already explains that.
func TestPollerSkipsGamesWithNoSource(t *testing.T) {
	c := &collector{last: map[string][]model.Mod{}, got: make(chan struct{}, 8)}

	ctx, cancel := context.WithCancel(context.Background())
	go (&mods.Poller{
		Interval: 10 * time.Millisecond,
		Refs:     stubRefs{refs: map[string][]model.ModRef{"valheim-main": {{ID: "1"}}}},
		Sources:  stubSources{resolver: nil},
	}).Run(ctx, c)

	time.Sleep(60 * time.Millisecond)
	cancel()

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, reported := c.last["valheim-main"]; reported {
		t.Error("a game with no mod source was reported on")
	}
}

func TestPollerWithNothingWiredReturns(t *testing.T) {
	done := make(chan struct{})
	go func() {
		(&mods.Poller{}).Run(context.Background(), nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a poller with nothing wired did not return")
	}
}

var _ = strings.TrimSpace
