package valheim

import (
	"bufio"
	"os"
	"path/filepath"
	"testing"

	"github.com/camden-brown/garrison/internal/model"
)

// The line format is captured, not documented. These are copied verbatim from
// testdata/session.log, which came off a real server — so a game update that
// renames a line fails here instead of silently emptying the Players view on a
// Tuesday evening.
func TestParseCapturedLines(t *testing.T) {
	tests := []struct {
		name    string
		line    string
		kind    model.Kind
		player  string
		steamID string
		metric  string
		value   float64
	}{
		{
			name: "socket opens with only a steam id",
			line: "Sep  9 19:57:20 supervisord: valheim-server 09/09/2026 19:57:20: Got connection SteamID 76561190000000001",
			// Not a join: the name does not exist yet.
			kind:    model.KindInfo,
			steamID: "76561190000000001",
		},
		{
			name:   "the character spawning is the join",
			line:   "Sep  9 19:58:17 supervisord: valheim-server 09/09/2026 19:58:17: Got character ZDOID from Dalinar : 2136340107:5",
			kind:   model.KindJoin,
			player: "Dalinar",
		},
		{
			name:    "leaving names no player, only an id",
			line:    "Sep  9 20:01:47 supervisord: valheim-server 09/09/2026 20:01:47: Closing socket 76561190000000001",
			kind:    model.KindLeave,
			steamID: "76561190000000001",
		},
		{
			name:   "the fifth save phase carries the total",
			line:   "Sep  9 20:02:14 supervisord: valheim-server 09/09/2026 20:02:14: World save (5/5) done. Total time [314ms]",
			kind:   model.KindSave,
			metric: MetricSaveMillis,
			value:  314,
		},
		{
			name: "world load",
			line: "Sep  9 19:56:15 supervisord: valheim-server 09/09/2026 19:56:15: ZNet.LoadWorld: Huldra (Huldra), save number 1",
			kind: model.KindInfo,
		},
		{
			name: "server ready",
			line: "Sep  9 19:56:24 supervisord: valheim-server 09/09/2026 19:56:24: Game server connected",
			kind: model.KindInfo,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Game{}.Parse(tt.line)

			if got.Kind != tt.kind {
				t.Errorf("Kind = %v, want %v", got.Kind, tt.kind)
			}
			if got.Player != tt.player {
				t.Errorf("Player = %q, want %q", got.Player, tt.player)
			}
			if got.SteamID != tt.steamID {
				t.Errorf("SteamID = %q, want %q", got.SteamID, tt.steamID)
			}
			if got.Metric != tt.metric {
				t.Errorf("Metric = %q, want %q", got.Metric, tt.metric)
			}
			if got.Value != tt.value {
				t.Errorf("Value = %v, want %v", got.Value, tt.value)
			}
			if got.Raw != tt.line {
				t.Errorf("Raw was not preserved for the console view")
			}
		})
	}
}

// The one branch not captured from a live server: a death reuses the join
// line with a zero object id. Marked as inferred rather than captured, because
// nobody died during the capture session.
func TestZeroZDOIsADeathNotAJoin(t *testing.T) {
	line := "Sep  9 19:58:17 supervisord: valheim-server 09/09/2026 19:58:17: Got character ZDOID from Dalinar : 0:0"

	got := Game{}.Parse(line)
	if got.Kind != model.KindDeath {
		t.Errorf("Kind = %v, want death — a zero ZDO id means the character object was destroyed", got.Kind)
	}
	if got.Player != "Dalinar" {
		t.Errorf("Player = %q, want Dalinar", got.Player)
	}
}

// The four earlier phases each carry their own timing and would each look like
// a completed save.
func TestOnlyTheFinalSavePhaseIsAMetric(t *testing.T) {
	earlier := []string{
		"Sep  9 20:02:14 supervisord: valheim-server 09/09/2026 20:02:14: World save (1/5) Cloud & Backup checks done [1ms] => Save number 2",
		"Sep  9 20:02:14 supervisord: valheim-server 09/09/2026 20:02:14: World save (2/5) Chunks writing done [265ms]",
		"Sep  9 20:02:14 supervisord: valheim-server 09/09/2026 20:02:14: World save (3/5) DB2 writing done [30ms]",
		"Sep  9 20:02:14 supervisord: valheim-server 09/09/2026 20:02:14: World save (4/5) FWL writing done [6ms]",
	}

	for _, line := range earlier {
		if got := (Game{}).Parse(line); got.Metric != "" {
			t.Errorf("an intermediate phase produced metric %q with value %v:\n%s", got.Metric, got.Value, line)
		}
	}
}

// The supervisord prefix belongs to the image, not the game. A different image
// wraps differently or not at all, so the raw line has to parse too.
func TestParseWithoutTheImagePrefix(t *testing.T) {
	got := Game{}.Parse("09/09/2026 19:58:17: Got character ZDOID from Dalinar : 2136340107:5")

	if got.Kind != model.KindJoin || got.Player != "Dalinar" {
		t.Errorf("unprefixed line parsed as %+v, want a join by Dalinar", got)
	}
}

// The timestamp Garrison keeps is Valheim's own, not the container's.
func TestParseUsesTheGamesTimestamp(t *testing.T) {
	got := Game{}.Parse("Sep  9 19:58:17 supervisord: valheim-server 09/09/2026 19:58:17: Game server connected")

	if got.At.IsZero() {
		t.Fatal("no timestamp parsed")
	}
	if h, m, s := got.At.Clock(); h != 19 || m != 58 || s != 17 {
		t.Errorf("At = %v, want 19:58:17", got.At)
	}
	if y, mo, d := got.At.Date(); y != 2026 || mo != 9 || d != 9 {
		t.Errorf("date = %v, want 2026-09-09 — the American ordering is Valheim's, not a typo", got.At)
	}
}

// Parse is called for every line of every running server. Most lines are
// Unity noise with no inner timestamp and must cost nothing and say nothing.
func TestUnclassifiableLinesAreDropped(t *testing.T) {
	noise := []string{
		"",
		"   ",
		"Sep  9 19:39:20 supervisord: valheim-server [AmplifyOcclusion] System does not support CopyTexture.",
		"Sep  9 19:38:32 supervisord: valheim-updater  Update state (0x61) downloading, progress: 62.55",
		"Unloading 302 unused Assets to reduce memory usage.",
		"Sep  9 19:38:52 supervisord: valheim-server Unable to load player prefs",
	}

	for _, line := range noise {
		if got := (Game{}).Parse(line); !got.Drop() {
			t.Errorf("line was classified as %v, want dropped:\n%q", got.Kind, line)
		}
	}
}

func TestParseIsTotalOverTheCapturedSession(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "session.log"))
	if err != nil {
		t.Fatalf("opening the fixture: %v", err)
	}
	defer f.Close()

	counts := map[model.Kind]int{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		counts[Game{}.Parse(scanner.Text()).Kind]++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}

	// One player joined once and left once, and the world was saved once.
	// If a future Valheim renames any of those lines these counts move, and
	// that is the whole point of the fixture.
	for kind, want := range map[model.Kind]int{
		model.KindJoin:  1,
		model.KindLeave: 1,
		model.KindSave:  1,
		model.KindDeath: 0,
	} {
		if counts[kind] != want {
			t.Errorf("the captured session yielded %d %v events, want %d", counts[kind], kind, want)
		}
	}
}
