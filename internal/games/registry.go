package games

import (
	"fmt"
	"sort"
	"sync"
)

var (
	mu       sync.RWMutex
	registry = make(map[string]Game)
)

// Register adds a game. Call it from a game package's init.
//
// It panics on a duplicate or empty ID because both can only happen at init
// time and are always a programming error, never a runtime condition.
func Register(g Game) {
	id := g.Meta().ID
	if id == "" {
		panic("games: Register called with an empty Meta().ID")
	}

	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[id]; dup {
		panic(fmt.Sprintf("games: duplicate registration for %q", id))
	}
	registry[id] = g
}

// Get returns the game registered under id.
func Get(id string) (Game, error) {
	mu.RLock()
	defer mu.RUnlock()
	g, ok := registry[id]
	if !ok {
		return nil, fmt.Errorf("games: no game registered as %q", id)
	}
	return g, nil
}

// IDs returns every registered game ID, sorted.
func IDs() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(registry))
	for id := range registry {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// All returns every registered game, ordered by ID.
func All() []Game {
	ids := IDs()
	out := make([]Game, 0, len(ids))
	for _, id := range ids {
		if g, err := Get(id); err == nil {
			out = append(out, g)
		}
	}
	return out
}
