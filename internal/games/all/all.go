// Package all blank-imports every game package so their init functions run.
//
// Adding a game to Garrison is: write internal/games/<name>, then add one line
// here. Nothing else in the codebase changes.
package all

import (
	_ "github.com/camden-brown/garrison/internal/games/valheim"
	_ "github.com/camden-brown/garrison/internal/games/zomboid"
)
