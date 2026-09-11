package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/games"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tui/comp"
)

// Sharing a server: "y" yanks the details a player needs into the clipboard.
//
// The text is also put on screen, and not as a courtesy. Copying from a
// terminal application means OSC 52, which the terminal is free to ignore —
// Windows Terminal supports it, an old PuTTY does not, and a locked-down SSH
// session may strip it. A feature whose only evidence of success is somebody
// else's paste is a feature that fails silently, so the panel shows exactly
// what was copied and stays up until dismissed.

// shareText is what goes on the clipboard.
//
// It is a pure function of the snapshot so it can be tested as a string rather
// than through a terminal, and so the panel and the clipboard cannot drift
// apart: both render this.
func shareText(srv core.Server, now string) string {
	var b strings.Builder

	name := srv.Name
	if g, err := games.Get(srv.Game); err == nil {
		if display := settingString(srv, "ServerName"); display != "" {
			name = display
		}
		fmt.Fprintf(&b, "%s (%s)\n", name, g.Meta().Name)
	} else {
		fmt.Fprintf(&b, "%s\n", name)
	}

	if addr := joinAddress(srv); addr != "" {
		fmt.Fprintf(&b, "Join: %s\n", addr)
	} else {
		// Saying what is missing beats pasting a LAN address that works for
		// nobody outside the house.
		b.WriteString("Join: not set — add address = \"host.example.org\" to the server's TOML\n")
	}

	if pass := settingString(srv, "ServerPass"); pass != "" {
		fmt.Fprintf(&b, "Password: %s\n", pass)
	} else {
		b.WriteString("Password: none\n")
	}

	if world := settingString(srv, "WorldName"); world != "" {
		fmt.Fprintf(&b, "World: %s\n", world)
	}

	fmt.Fprintf(&b, "Status: %s as of %s\n", srv.State.String(), now)

	b.WriteString(modsText(srv))
	return b.String()
}

// modsText is the half a player needs before they can connect at all.
//
// A server's mods are not a detail of the server, they are a prerequisite for
// joining it: for a game whose mods are files the client must have the same
// ones at the same versions, and the usual failure is a connection refused
// with no explanation. So the share carries the list and the steps, and the
// steps come from the game rather than from here.
func modsText(srv core.Server) string {
	mods := srv.Mods
	if len(mods) == 0 {
		// Nothing resolved yet — the poller may not have run. The configured
		// ids are still worth pasting; a name is nicer than an id, but an id
		// is what somebody types into a search box anyway.
		for _, ref := range srv.Instance.Mods {
			mods = append(mods, model.Mod{ID: ref.ID, Pin: ref.Pin})
		}
	}
	if len(mods) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "\nMods (%d) — install these before joining:\n", len(mods))
	for _, m := range mods {
		line := "  " + m.ID
		if v := installedVersion(m); v != "" {
			line += " " + v
		}
		b.WriteString(line + "\n")
	}

	if code := srv.Instance.ModProfile; code != "" {
		fmt.Fprintf(&b, "\nMod profile code: %s\n", code)
	}

	g, err := games.Get(srv.Game)
	if err != nil {
		return b.String()
	}
	installable, ok := g.(games.Installable)
	if !ok {
		// A game whose server hands its mods to the client — Zomboid
		// subscribes them through Steam — needs no instructions, and
		// inventing some would be the shell guessing on a plugin's behalf.
		return b.String()
	}
	steps := installable.ClientSteps(srv.Instance.ModProfile)
	if len(steps) == 0 {
		return b.String()
	}

	b.WriteString("\nHow to install:\n")
	for i, step := range steps {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, step)
	}
	return b.String()
}

// installedVersion is the version a player has to match, which is the one the
// server is running rather than the newest one published.
func installedVersion(m model.Mod) string {
	if m.Version != "" {
		return m.Version
	}
	// A pin is what the configuration asked for, and before the first
	// install it is all anybody knows.
	return m.Pin
}

// joinAddress is what a player types, empty when nobody has said.
//
// The port is the first published one, which is the game port for every game
// Garrison has met. A game that wants a different one — a query port, or a
// second address entirely — is a games capability rather than a special case
// here, and it can have one when it turns up.
func joinAddress(srv core.Server) string {
	host := srv.Instance.Address
	if host == "" {
		return ""
	}
	for _, p := range srv.Ports {
		return fmt.Sprintf("%s:%d", host, p.Host)
	}
	for _, p := range srv.Instance.Ports {
		return fmt.Sprintf("%s:%d", host, p.Host)
	}
	return host
}

// settingString reads a setting as text, empty when absent or another type.
func settingString(srv core.Server, key string) string {
	v, ok := srv.Setting(key)
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// share opens the panel and copies.
func (a *App) share() tea.Cmd {
	srv, ok := a.snap.Server(a.selected)
	if !ok {
		a.store.Notify(a.ctx, "", "pick a server first — there is nothing to share about the fleet.")
		return nil
	}

	text := shareText(srv, a.Now().Format("15:04"))
	a.sharing = text

	// The write goes out as a command rather than inline, so it lands
	// between renders instead of in the middle of one.
	return func() tea.Msg {
		termenv.Copy(text)
		return nil
	}
}

// shareView renders what was copied.
func (a *App) shareView(width int) string {
	t := a.theme

	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(a.sharing, "\n"), "\n") {
		label, rest, found := strings.Cut(line, ": ")
		// Only the header's "Label: value" lines are laid out in two
		// columns. A step reading "Install r2modman: https://..." has a
		// colon in it too, and putting a sentence in a ten-column label
		// makes a mess of the one part somebody has to read carefully.
		if !found || strings.ContainsAny(label, " \t") {
			b.WriteString(t.Title.Render(comp.Truncate(line, comp.Inner(width))))
			b.WriteString("\n")
			continue
		}
		b.WriteString(t.Dim.Render(comp.Pad(label, 10)))
		b.WriteString(t.Title.Render(comp.Truncate(rest, comp.Inner(width)-10)))
		b.WriteString("\n")
	}

	b.WriteString(t.Dim.Render(comp.Truncate(
		"Copied. If your terminal ignores OSC 52, select the text above instead. Any key closes.",
		comp.Inner(width))))

	return comp.Panel{
		Theme: t, Title: "SHARE", Right: "for posting to players",
		Width: width, Focused: true,
	}.Render(strings.TrimRight(b.String(), "\n"))
}

// shareRows is how much of the stage the panel takes.
//
// It grows with the text, because the mod list and its instructions are as
// long as the server has mods — a fixed height would quietly cut off the
// steps, which are the part a player actually needs.
func shareRows(text string) int {
	const chrome = 4 // panel border, title row, the hint line
	rows := strings.Count(strings.TrimRight(text, "\n"), "\n") + 1 + chrome
	if rows < minShareRows {
		return minShareRows
	}
	return rows
}

const minShareRows = 9
