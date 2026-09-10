package zomboid

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/camden-brown/garrison/internal/games"
	"github.com/camden-brown/garrison/internal/model"
)

// The first game to implement any of caps.go, which is the other half of what
// M3 was for. Four capabilities, and each one is only a few lines — because
// the transport is the caller's and the plugin only has to know what to say.
var (
	_ games.Rostered    = Game{}
	_ games.Commandable = Game{}
	_ games.Drainable   = Game{}
	_ games.Moddable    = Game{}
)

// Roster asks the server who is connected.
//
// This exists because Parse cannot answer it: Project Zomboid's join and leave
// lines never reach stdout. Valheim reconstructs a roster from its log and
// mis-pairs two players who load out of order; this one asks and is told, so
// the pairing debt simply does not apply to this game.
//
// The reply is a header and one name per line:
//
//	Players connected (2):
//	-Huldra
//	-Bjorn
func (Game) Roster(ctx context.Context, c games.Conn) ([]model.Player, error) {
	out, err := c.RCON(ctx, "players")
	if err != nil {
		return nil, fmt.Errorf("asking who is connected: %w", err)
	}

	var players []model.Player
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "-") {
			// The count header, or a blank line. The count is not used: the
			// names are the answer and a disagreement between them would be
			// a header worth ignoring rather than an error worth raising.
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if name == "" {
			continue
		}
		players = append(players, model.Player{Name: name})
	}
	return players, nil
}

// Command sends one console command and returns what the server said.
//
// The console view's input routes here. Nothing is filtered: an operator with
// RCON access can already do anything this could refuse, so a denylist would
// be theatre — and the one thing worth refusing, an empty command, is refused
// because the server answers it with silence that reads as a hang.
func (Game) Command(ctx context.Context, c games.Conn, cmd string) (string, error) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", errors.New("nothing to send")
	}
	out, err := c.RCON(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("%q: %w", cmd, err)
	}
	if strings.TrimSpace(out) == "" {
		// Several commands succeed silently. Saying so beats a blank line
		// that is indistinguishable from a dropped connection.
		return "(no output)", nil
	}
	return out, nil
}

// Complete offers the commands worth having tab-completion for.
//
// Deliberately not the full list the server accepts. A completion list is a
// suggestion, and suggesting "grantadmin" a keystroke away from "godmode" on
// a live server is not a kindness — these are the ones an operator runs on
// purpose rather than the ones the server happens to expose.
func (Game) Complete(prefix string) []string {
	commands := []string{
		"players", "save", "servermsg", "kick", "quit",
		"checkModsNeedUpdate", "reloadoptions", "showoptions",
		"addalltowhitelist", "banid", "unbanid", "voiceban",
	}
	if prefix == "" {
		return commands
	}

	var out []string
	lower := strings.ToLower(prefix)
	for _, c := range commands {
		if strings.HasPrefix(strings.ToLower(c), lower) {
			out = append(out, c)
		}
	}
	return out
}

// Warn tells everyone connected that the server is going down.
//
// This is the capability drain was waiting for, and the reason Valheim cannot
// have it: there is no channel to say anything to a Valheim player on, so a
// restart there is a restart that arrives without warning. Here it is one
// command.
func (Game) Warn(ctx context.Context, c games.Conn, in time.Duration) error {
	msg := fmt.Sprintf("Server restarting in %s. Find somewhere safe.", humanDuration(in))
	if in <= 0 {
		msg = "Server restarting now."
	}

	// The quotes are the server's own requirement: servermsg takes one
	// quoted argument and treats an unquoted one as the first word only.
	if _, err := c.RCON(ctx, fmt.Sprintf("servermsg %q", msg)); err != nil {
		return fmt.Errorf("warning players: %w", err)
	}
	return nil
}

// Save flushes the world to disk.
//
// A drain warns, waits, and then saves before the stop signal, so the world on
// disk is one a player would recognise rather than whatever the last autosave
// caught.
func (Game) Save(ctx context.Context, c games.Conn) error {
	if _, err := c.RCON(ctx, "save"); err != nil {
		return fmt.Errorf("saving the world: %w", err)
	}
	return nil
}

// humanDuration is the phrasing a warning uses: whole units, because "15m0s"
// in a message to players reads as a machine talking.
func humanDuration(d time.Duration) string {
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	case d >= time.Minute:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	default:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	}
}

// ModSource is the Steam Workshop.
func (Game) ModSource() games.ModSource { return games.ModSourceWorkshop }

// LoadOrderMatters is true, and this is the game that makes the flag worth
// having.
//
// Project Zomboid loads mods in the order WorkshopItems lists them, and a
// dependency listed after the mod that needs it is a server that will not
// start. The Mods view numbers the list because of this answer and does not
// for a game that returns false.
func (Game) LoadOrderMatters() bool { return true }

// Apply declares the mods in config, which is what this interface is shaped
// for and why Valheim could not implement it.
//
// The server downloads Workshop items itself given their ids, so Garrison
// never fetches a mod: it writes two keys and the server does the rest. Mods
// names the folders to load and WorkshopItems names the ids to subscribe to,
// and both have to agree or the server subscribes to something it then does
// not load.
func (Game) Apply(inst model.Instance, mods []games.Mod) ([]model.File, error) {
	ids := make([]string, 0, len(mods))
	names := make([]string, 0, len(mods))
	for _, m := range mods {
		if !m.Enabled {
			continue
		}
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
		if m.Name != "" {
			names = append(names, m.Name)
		}
	}

	// Applying mods is applying settings: the two keys go through the same
	// Compile as everything else, so the diff the settings screen shows
	// covers a mod change too rather than being a separate path nobody sees.
	next := inst
	settings := make(map[string]any, len(inst.Settings)+2)
	for k, v := range inst.Settings {
		settings[k] = v
	}
	settings["WorkshopItems"] = strings.Join(ids, ";")
	settings["Mods"] = strings.Join(names, ";")
	next.Settings = settings

	return Game{}.Compile(next)
}
