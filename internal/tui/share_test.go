package tui

import (
	"strings"
	"testing"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/model"

	_ "github.com/camden-brown/garrison/internal/games/all"
)

func server(game string, mods []model.Mod, refs ...model.ModRef) core.Server {
	return core.Server{
		Name: "server-one", Game: game, State: model.StateRunning,
		Instance: model.Instance{
			Name: "server-one", Game: game, Address: "example.org",
			Ports:    []model.PortMap{{Container: "2456/udp", Host: 2456}},
			Settings: map[string]any{"ServerName": "The Shattered Plains"},
			Mods:     refs,
		},
		Mods: mods,
	}
}

// A server's mods are not a detail of the server, they are a prerequisite for
// joining it: the client needs the same ones at the same versions, and the
// failure when it does not is a refused connection with no explanation.
func TestShareCarriesTheModsAndHowToInstallThem(t *testing.T) {
	text := shareText(server("valheim", []model.Mod{
		{ID: "Azumatt-AzuExtendedPlayerInventory", Version: "2.4.8"},
		{ID: "Azumatt-AzuCraftyBoxes", Version: "1.8.15"},
	}), "08:30")

	for _, want := range []string{
		"Azumatt-AzuExtendedPlayerInventory 2.4.8",
		"Azumatt-AzuCraftyBoxes 1.8.15",
		"r2modman",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the share does not mention %q:\n%s", want, text)
		}
	}

	// The version that matters is the one the server runs, not the newest
	// published — matching it is the whole point.
	if strings.Contains(text, "latest") {
		t.Errorf("the share offers a moving target:\n%s", text)
	}
}

// Before the first resolve there are ids and nothing else, and an id is what
// somebody types into a search box anyway.
func TestShareFallsBackToTheConfiguredIds(t *testing.T) {
	text := shareText(server("valheim", nil, model.ModRef{ID: "Owner-Package"}), "08:30")
	if !strings.Contains(text, "Owner-Package") {
		t.Errorf("an unresolved mod is missing from the share:\n%s", text)
	}
}

// A game whose server hands its mods to the client needs no instructions, and
// inventing some would be the shell guessing on a plugin's behalf.
func TestShareOmitsStepsForAGameThatInstallsForYou(t *testing.T) {
	text := shareText(server("zomboid", []model.Mod{{ID: "2822286426", Version: "1.0"}}), "08:30")
	if !strings.Contains(text, "2822286426") {
		t.Errorf("the mod list is missing:\n%s", text)
	}
	if strings.Contains(text, "How to install") {
		t.Errorf("Zomboid subscribes its own mods; the share should not tell a player to:\n%s", text)
	}
}

// An unmodded server's share is what it always was.
func TestShareOfAnUnmoddedServerSaysNothingAboutMods(t *testing.T) {
	text := shareText(server("valheim", nil), "08:30")
	if strings.Contains(text, "Mods") {
		t.Errorf("a server with no mods talks about mods:\n%s", text)
	}
}

// A fixed panel height would cut off the steps, which are the part a player
// actually needs.
func TestTheSharePanelGrowsWithTheText(t *testing.T) {
	short := shareRows(shareText(server("valheim", nil), "08:30"))
	long := shareRows(shareText(server("valheim", []model.Mod{
		{ID: "Azumatt-AzuExtendedPlayerInventory", Version: "2.4.8"},
		{ID: "Azumatt-AzuCraftyBoxes", Version: "1.8.15"},
	}), "08:30"))

	if long <= short {
		t.Errorf("panel is %d rows with mods and %d without, want it to grow", long, short)
	}
	if short < minShareRows {
		t.Errorf("panel is %d rows, want at least %d", short, minShareRows)
	}
}
