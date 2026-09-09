// Command garrison is a terminal dashboard for running game servers in Docker.
//
// With no arguments it opens the TUI. Every action in the TUI is also a
// subcommand, so a scheduled job can reach it — the TUI and the CLI are peers
// over internal/core, not wrappers around each other.
//
// Neither exists yet: this is the M0 skeleton. See docs/DESIGN.md.
package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/camden-brown/garrison/internal/games"

	// Registers every game. Adding one is a line in that package.
	_ "github.com/camden-brown/garrison/internal/games/all"
)

// version is set at build time: -ldflags "-X main.version=$(git describe --tags)"
var version = "dev"

func main() {
	fmt.Printf("garrison %s  %s/%s\n", version, runtime.GOOS, runtime.GOARCH)

	ids := games.IDs()
	if len(ids) == 0 {
		fmt.Println("\nno games registered yet.")
		fmt.Println("next: internal/host/docker (M0), then internal/games/valheim (M1).")
		return
	}

	fmt.Printf("\n%d game(s) registered:\n", len(ids))
	for _, g := range games.All() {
		m := g.Meta()
		fmt.Printf("  %-10s %-22s steam %s\n", m.ID, m.Name, m.SteamAppID)

		caps := capabilities(g)
		if len(caps) == 0 {
			fmt.Println("             (no optional capabilities)")
			continue
		}
		fmt.Printf("             %s\n", strings.Join(caps, " · "))
	}

	if len(os.Args) > 1 {
		fmt.Fprintf(os.Stderr, "\nno subcommands yet: %q\n", os.Args[1:])
		os.Exit(2)
	}
}

// capabilities reports which optional interfaces a game implements. The same
// assertions gate views in the TUI, which is how a game with no mods gets an
// explanation instead of an empty table.
func capabilities(g games.Game) []string {
	var out []string
	if _, ok := g.(games.Rostered); ok {
		out = append(out, "rostered")
	}
	if _, ok := g.(games.Commandable); ok {
		out = append(out, "commandable")
	}
	if _, ok := g.(games.Drainable); ok {
		out = append(out, "drainable")
	}
	if _, ok := g.(games.Moddable); ok {
		out = append(out, "moddable")
	}
	if _, ok := g.(games.Backupable); ok {
		out = append(out, "backupable")
	}
	if _, ok := g.(games.Probeable); ok {
		out = append(out, "probeable")
	}
	return out
}
