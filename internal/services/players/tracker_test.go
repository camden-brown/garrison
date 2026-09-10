package players_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/services/players"
)

var at = time.Date(2026, 9, 9, 21, 7, 0, 0, time.UTC)

// fakeStore is the durable half, in memory.
type fakeStore struct {
	mu       sync.Mutex
	next     int64
	sessions map[int64]*model.Session
	opens    int
	closes   int
}

func newFakeStore() *fakeStore {
	return &fakeStore{sessions: map[int64]*model.Session{}}
}

func (f *fakeStore) SessionOpened(server string, p model.Player, when time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.opens++
	f.sessions[f.next] = &model.Session{
		Server: server, Player: p.Name, SteamID: p.SteamID, Joined: when,
	}
	return f.next, nil
}

func (f *fakeStore) SessionClosed(id int64, when time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
	if s, ok := f.sessions[id]; ok {
		s.Left = when
	}
	return nil
}

func (f *fakeStore) SessionsSince(server string, _ time.Time) ([]model.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.Session
	for _, s := range f.sessions {
		if s.Server == server {
			out = append(out, *s)
		}
	}
	return out, nil
}

func (f *fakeStore) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens, f.closes
}

func (f *fakeStore) openCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.sessions {
		if s.Open() {
			n++
		}
	}
	return n
}

// fakeFleet is a roster the test moves under the tracker.
type fakeFleet struct {
	mu      sync.Mutex
	rosters map[string][]model.Player
}

func (f *fakeFleet) Rosters() map[string][]model.Player {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string][]model.Player{}
	for k, v := range f.rosters {
		out[k] = append([]model.Player(nil), v...)
	}
	return out
}

func (f *fakeFleet) set(server string, players ...model.Player) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rosters[server] = players
}

type collector struct {
	mu   sync.Mutex
	last map[string][]model.Session
	got  chan struct{}
}

func newCollector() *collector {
	return &collector{last: map[string][]model.Session{}, got: make(chan struct{}, 64)}
}

func (c *collector) SessionsListed(_ context.Context, _ time.Time, server string, sessions []model.Session) {
	c.mu.Lock()
	c.last[server] = sessions
	c.mu.Unlock()
	select {
	case c.got <- struct{}{}:
	default:
	}
}

func (c *collector) wait(t *testing.T) {
	t.Helper()
	select {
	case <-c.got:
	case <-time.After(2 * time.Second):
		t.Fatal("the tracker never reported")
	}
}

// tick runs one pass by hand rather than waiting on the ticker, so the tests
// are deterministic instead of timing-dependent.
func tick(t *testing.T, tr *players.Tracker, c *collector) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	go tr.Run(ctx, c)
	c.wait(t)
	cancel()
}

func TestTrackerOpensASessionWhenSomebodyArrives(t *testing.T) {
	fleet := &fakeFleet{rosters: map[string][]model.Player{}}
	fleet.set("a", model.Player{Name: "Huldra", SteamID: "76561", Since: at})
	store := newFakeStore()

	tr := &players.Tracker{Interval: time.Hour, Fleet: fleet, Store: store}
	c := newCollector()
	tick(t, tr, c)

	if opens, closes := store.counts(); opens != 1 || closes != 0 {
		t.Errorf("opens=%d closes=%d, want 1 and 0", opens, closes)
	}

	// The session starts when the player joined, not when the tracker
	// noticed — restarting Garrison must not restart everyone's clock.
	sessions, _ := store.SessionsSince("a", time.Time{})
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(sessions))
	}
	if !sessions[0].Joined.Equal(at) {
		t.Errorf("session started at %v, want the join time %v", sessions[0].Joined, at)
	}
}

// The same player across two polls is one session, not two.
func TestTrackerDoesNotReopenAStayingPlayer(t *testing.T) {
	fleet := &fakeFleet{rosters: map[string][]model.Player{}}
	fleet.set("a", model.Player{Name: "Huldra", SteamID: "76561", Since: at})
	store := newFakeStore()

	tr := &players.Tracker{Interval: time.Hour, Fleet: fleet, Store: store}
	c := newCollector()
	tick(t, tr, c)
	tick(t, tr, c)
	tick(t, tr, c)

	if opens, _ := store.counts(); opens != 1 {
		t.Errorf("opened %d sessions for one continuous player, want 1", opens)
	}
}

func TestTrackerClosesASessionWhenSomebodyLeaves(t *testing.T) {
	fleet := &fakeFleet{rosters: map[string][]model.Player{}}
	fleet.set("a", model.Player{Name: "Huldra", SteamID: "76561", Since: at})
	store := newFakeStore()

	tr := &players.Tracker{Interval: time.Hour, Fleet: fleet, Store: store}
	c := newCollector()
	tick(t, tr, c)

	fleet.set("a") // everyone left
	tick(t, tr, c)

	if opens, closes := store.counts(); opens != 1 || closes != 1 {
		t.Errorf("opens=%d closes=%d, want 1 and 1", opens, closes)
	}
	if store.openCount() != 0 {
		t.Error("a session was left open after the player left")
	}
}

// A player who changes their display name mid-session is the same player. The
// Steam id is the identity; keying on the name would record a departure and an
// arrival that never happened.
func TestTrackerFollowsAPlayerThroughARename(t *testing.T) {
	fleet := &fakeFleet{rosters: map[string][]model.Player{}}
	fleet.set("a", model.Player{Name: "Huldra", SteamID: "76561", Since: at})
	store := newFakeStore()

	tr := &players.Tracker{Interval: time.Hour, Fleet: fleet, Store: store}
	c := newCollector()
	tick(t, tr, c)

	fleet.set("a", model.Player{Name: "Huldra the Bold", SteamID: "76561", Since: at})
	tick(t, tr, c)

	if opens, closes := store.counts(); opens != 1 || closes != 0 {
		t.Errorf("opens=%d closes=%d after a rename, want 1 and 0", opens, closes)
	}
}

// A game with no Steam ids at all still has to work — the name is the
// fallback identity.
func TestTrackerHandlesPlayersWithNoID(t *testing.T) {
	fleet := &fakeFleet{rosters: map[string][]model.Player{}}
	fleet.set("a", model.Player{Name: "Huldra", Since: at})
	store := newFakeStore()

	tr := &players.Tracker{Interval: time.Hour, Fleet: fleet, Store: store}
	c := newCollector()
	tick(t, tr, c)
	tick(t, tr, c)

	if opens, _ := store.counts(); opens != 1 {
		t.Errorf("opened %d sessions for one named player, want 1", opens)
	}
}

func TestTrackerReportsTheHistory(t *testing.T) {
	fleet := &fakeFleet{rosters: map[string][]model.Player{}}
	fleet.set("a", model.Player{Name: "Huldra", SteamID: "76561", Since: at})
	store := newFakeStore()

	tr := &players.Tracker{Interval: time.Hour, Fleet: fleet, Store: store}
	c := newCollector()
	tick(t, tr, c)

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.last["a"]) != 1 {
		t.Errorf("reported %d sessions, want 1", len(c.last["a"]))
	}
}

func TestTrackerWithNothingWiredReturns(t *testing.T) {
	done := make(chan struct{})
	go func() {
		(&players.Tracker{}).Run(context.Background(), nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a tracker with nothing wired did not return")
	}
}
