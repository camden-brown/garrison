package zomboid

import (
	"bufio"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/games"
	"github.com/camden-brown/garrison/internal/model"
)

func instance() model.Instance {
	return model.Instance{
		Name: "zomboid-main",
		Game: "zomboid",
		Data: `C:\gameservers\zomboid-main`,
		Resources: model.Resources{
			Memory: 12 << 30, // 12 GiB
		},
		Settings: map[string]any{
			"PublicName":      "Knox County",
			"MaxPlayers":      24,
			"PVP":             false,
			"Open":            true,
			"RCONPassword":    "fixture-rcon",
			"Sandbox.Zombies": 3,
		},
	}
}

// ---- Plan ---------------------------------------------------------------

// The image rewrites thirteen config keys from environment variables on every
// boot. If Plan and Compile disagree about any of them, the file says one
// thing and the running server another, and the settings screen agrees with
// neither.
func TestPlanMirrorsTheKeysTheImageRewrites(t *testing.T) {
	plan, err := (Game{}).Plan(instance())
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}

	for key, want := range map[string]string{
		"SERVER_NAME":   "Knox County",
		"MAX_PLAYERS":   "24",
		"PUBLIC_SERVER": "true",
		"RCON_PASSWORD": "fixture-rcon",
		"DEFAULT_PORT":  "16261",
		"UDP_PORT":      "16262",
		"RCON_PORT":     "27015",
	} {
		if got := plan.Env[key]; got != want {
			t.Errorf("Env[%q] = %q, want %q", key, got, want)
		}
	}
}

// An RCON port open to the image's default password is worse than one that
// does not work, because the failure is silent and remote.
func TestPlanNeverFallsBackToTheImagesRCONPassword(t *testing.T) {
	inst := instance()
	delete(inst.Settings, "RCONPassword")

	plan, _ := (Game{}).Plan(inst)
	if got := plan.Env["RCON_PASSWORD"]; got == "changeme_rcon" {
		t.Error("Plan fell back to the image's default RCON password")
	}
}

// Two numbers that must agree and can be edited apart will disagree, and the
// way that surfaces is a server the kernel kills mid-save.
func TestPlanDerivesTheHeapFromTheMemoryLimit(t *testing.T) {
	plan, _ := (Game{}).Plan(instance())

	// 12 GiB, three quarters: 9216m.
	if got := plan.Env["MAX_RAM"]; got != "9216m" {
		t.Errorf("MAX_RAM = %q, want 9216m — three quarters of the limit", got)
	}

	// An uncapped container still needs a number, and it must not be zero.
	bare := instance()
	bare.Resources.Memory = 0
	plan, _ = (Game{}).Plan(bare)
	if got := plan.Env["MAX_RAM"]; got == "0m" || got == "" {
		t.Errorf("MAX_RAM = %q for an uncapped container", got)
	}
}

// Two minutes, because a populated Knox County takes far longer to write than
// a Valheim seed and a save killed halfway is the failure this avoids.
func TestPlanAllowsTimeToSave(t *testing.T) {
	plan, _ := (Game{}).Plan(instance())
	if plan.StopGrace < 120*time.Second {
		t.Errorf("StopGrace = %v, too short for a populated world", plan.StopGrace)
	}
}

// ---- Compile ------------------------------------------------------------

func compiled(t *testing.T, inst model.Instance) map[string]string {
	t.Helper()
	files, err := (Game{}).Compile(inst)
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	out := map[string]string{}
	for _, f := range files {
		out[f.Path] = string(f.Data)
	}
	return out
}

// Two files in two syntaxes, which is the thing M3 exists to test. Valheim
// compiles to nothing, so this is the first time Compile has had to be right
// about anything.
func TestCompileWritesBothFiles(t *testing.T) {
	files := compiled(t, instance())

	ini, ok := files["Server/servertest.ini"]
	if !ok {
		t.Fatalf("no ini file; got %v", keys(files))
	}
	lua, ok := files["Server/servertest_SandboxVars.lua"]
	if !ok {
		t.Fatalf("no sandbox file; got %v", keys(files))
	}

	if !strings.Contains(ini, "PublicName=Knox County") {
		t.Errorf("the ini does not carry the server name:\n%s", head(ini))
	}
	if !strings.Contains(ini, "MaxPlayers=24") {
		t.Errorf("the ini does not carry MaxPlayers:\n%s", head(ini))
	}
	if !strings.Contains(lua, "SandboxVars = {") || !strings.HasSuffix(strings.TrimSpace(lua), "}") {
		t.Errorf("the sandbox file is not a Lua table:\n%s", head(lua))
	}
	if !strings.Contains(lua, "Zombies = 3,") {
		t.Errorf("the sandbox file does not carry the edited setting:\n%s", head(lua))
	}
}

// The decision ADR 0011 records: both files are written whole. A file
// assembled from only the keys Garrison models would delete the other four
// hundred, and this server does not accept a partial one.
func TestCompileWritesEveryKeyItKnowsAbout(t *testing.T) {
	files := compiled(t, instance())
	ini := files["Server/servertest.ini"]
	lua := files["Server/servertest_SandboxVars.lua"]

	for _, s := range iniSettings {
		if !strings.Contains(ini, s.Key+"=") {
			t.Fatalf("ini is missing %q — writing a partial file deletes the rest", s.Key)
		}
	}
	if got := strings.Count(ini, "="); got < len(iniSettings) {
		t.Errorf("ini has %d assignments, want at least %d", got, len(iniSettings))
	}

	// The sandbox file nests, so the check is on the leaf name.
	for _, s := range sandboxSettings {
		leaf := s.Key
		if _, after, nested := strings.Cut(s.Key, "."); nested {
			leaf = after
		}
		if !strings.Contains(lua, leaf+" = ") {
			t.Fatalf("sandbox file is missing %q", s.Key)
		}
	}
}

// An unedited instance compiles to the game's own defaults, so a server built
// by Garrison starts the way the game intended rather than the way a
// half-filled struct did.
func TestCompileFallsBackToTheGamesDefaults(t *testing.T) {
	files := compiled(t, model.Instance{Name: "bare", Game: "zomboid"})
	lua := files["Server/servertest_SandboxVars.lua"]

	// The Apocalypse preset's zombie population is 4 (Normal).
	if !strings.Contains(lua, "Zombies = 4,") {
		t.Errorf("an unedited instance did not get the game's default:\n%s", head(lua))
	}
}

// A whole number must not come out as "4.0". The sandbox file is diffed
// against one the server wrote, and a float suffix on every integer is a diff
// nobody reads.
func TestCompileDoesNotWriteFloatSuffixesOnWholeNumbers(t *testing.T) {
	inst := instance()
	// TOML decodes a bare number as float64, which is how this reaches us.
	inst.Settings["Sandbox.Zombies"] = float64(2)

	lua := compiled(t, inst)["Server/servertest_SandboxVars.lua"]
	if strings.Contains(lua, "Zombies = 2.0") {
		t.Errorf("a whole number was written as a float:\n%s", head(lua))
	}
	if !strings.Contains(lua, "Zombies = 2,") {
		t.Errorf("the value did not survive:\n%s", head(lua))
	}
}

// A line break in an ini value would end the line early and move the rest
// into a key the server does not know. The file has no escape for it, so it
// is refused rather than mangled.
func TestCompileRefusesALineBreakInTheINI(t *testing.T) {
	inst := instance()
	inst.Settings["PublicName"] = "Knox\nCounty"

	if _, err := (Game{}).Compile(inst); err == nil {
		t.Error("a line break was accepted into servertest.ini")
	}
}

// The Lua file does have an escape, so a quote in a welcome message is
// representable rather than a syntax error the server reports as a missing
// sandbox file.
func TestCompileEscapesLuaStrings(t *testing.T) {
	// Find a string-typed sandbox setting to put a quote in.
	var key string
	for _, s := range sandboxSettings {
		if s.Type == "string" {
			key = s.Key
			break
		}
	}
	if key == "" {
		t.Skip("no string-typed sandbox setting to test with")
	}

	inst := instance()
	inst.Settings[sandboxPrefix+key] = `say "hi"`

	lua := compiled(t, inst)["Server/servertest_SandboxVars.lua"]
	if !strings.Contains(lua, `\"hi\"`) {
		t.Errorf("a quote was not escaped:\n%s", head(lua))
	}
}

// ---- Schema -------------------------------------------------------------

// The form and Compile are built from the same table, so they cannot disagree
// about what exists.
func TestSchemaCoversEveryCompiledKey(t *testing.T) {
	fields := map[string]bool{}
	for _, f := range (Game{}).Schema().Fields {
		fields[f.Key] = true
	}

	for _, s := range iniSettings {
		if !fields[s.Key] {
			t.Errorf("ini key %q is compiled but not in the schema", s.Key)
		}
	}
	for _, s := range sandboxSettings {
		if !fields[sandboxPrefix+s.Key] {
			t.Errorf("sandbox key %q is compiled but not in the schema", s.Key)
		}
	}
}

// 413 fields is only usable because almost all of them are Advanced. A form
// that shows everything at once is a config file with borders.
func TestSchemaPromotesOnlyAHandful(t *testing.T) {
	var shown int
	for _, f := range (Game{}).Schema().Fields {
		if !f.Advanced {
			shown++
		}
	}
	if shown > 40 {
		t.Errorf("%d fields are shown without pressing a, which is too many to read", shown)
	}
	if shown < 5 {
		t.Errorf("only %d fields are shown, which hides the common ones", shown)
	}
}

// The graded settings come from the game's own numbering, documented in the
// config the server writes. An option the server would not accept must not be
// offered — the same rule ADR 0008 set for Valheim's modifiers.
func TestSchemaEnumsComeFromTheGame(t *testing.T) {
	byKey := map[string]setting{}
	for _, s := range sandboxSettings {
		byKey[sandboxPrefix+s.Key] = s
	}

	var enums int
	for _, f := range (Game{}).Schema().Fields {
		if f.Type != games.TypeEnum {
			continue
		}
		enums++

		s, ok := byKey[f.Key]
		if !ok {
			t.Errorf("%q is an enum with no generated setting behind it", f.Key)
			continue
		}
		if len(f.Options) != len(s.Options) {
			t.Errorf("%q offers %d options, the game documents %d", f.Key, len(f.Options), len(s.Options))
		}
	}
	if enums == 0 {
		t.Error("no enum fields at all, so the extracted vocabularies are unused")
	}
}

// Map changes which world the server opens; ResetID makes every client
// re-download it. Both cost a world and both get the typed confirmation.
func TestTheWorldLosingSettingsAreMarked(t *testing.T) {
	want := map[string]bool{"Map": true, "ResetID": true}
	for _, f := range (Game{}).Schema().Fields {
		if want[f.Key] && f.Impact != games.ImpactWipeRisk {
			t.Errorf("%q has impact %v, want WipeRisk", f.Key, f.Impact)
		}
	}
}

func TestPasswordsAreSecrets(t *testing.T) {
	for _, f := range (Game{}).Schema().Fields {
		if strings.Contains(strings.ToLower(f.Key), "password") && f.Type != games.TypeSecret {
			t.Errorf("%q is a password but not TypeSecret", f.Key)
		}
	}
}

func TestLabelsAreReadable(t *testing.T) {
	for key, want := range map[string]string{
		"MaxPlayers":            "Max players",
		"SaveWorldEveryMinutes": "Save world every minutes",
		"RCONPort":              "RCONPort",
		"PVP":                   "PVP",
	} {
		if got := label(key); got != want {
			t.Errorf("label(%q) = %q, want %q", key, got, want)
		}
	}
}

// ---- Parse --------------------------------------------------------------

// The fixture is a captured session from a real server. What is not in it is
// the point: no join, leave or chat lines, because the server writes those to
// files inside the volume and they never reach stdout.
func TestParseTheCapturedSession(t *testing.T) {
	f, err := os.Open("testdata/session.log")
	if err != nil {
		t.Fatalf("opening the fixture: %v", err)
	}
	defer f.Close()

	var classified, unclassified int
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := scan.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		ev := (Game{}).Parse(line)
		if ev.Drop() {
			unclassified++
			continue
		}
		classified++

		if ev.At.IsZero() {
			t.Errorf("no timestamp parsed from %q", line)
		}
		if ev.Text == "" {
			t.Errorf("no message extracted from %q", line)
		}
		// The bookkeeping between the channel and the message must not
		// survive into what a person reads.
		if strings.Contains(ev.Text, "st:") || strings.Contains(ev.Text, "f:0") {
			t.Errorf("the frame counter leaked into the message: %q", ev.Text)
		}
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}

	if classified == 0 {
		t.Fatal("nothing in the captured session was classified")
	}
	if unclassified > classified {
		t.Errorf("%d lines classified and %d dropped — the prefix parse is wrong", classified, unclassified)
	}
}

func TestParseSeverities(t *testing.T) {
	for line, want := range map[string]model.Kind{
		"[26-08-26 11:37:19.641] LOG  : General      f:0 st:1,2,3> world loaded.":                model.KindInfo,
		"[26-08-26 11:37:36.269] WARN : Script       f:0 st:1,2,3> unknown script object \"/\".": model.KindWarn,
		"[26-08-26 11:37:40.000] ERROR: General      f:0 st:1,2,3> could not load a chunk.":      model.KindError,
		"[26-08-26 11:37:41.000] LOG  : General      f:0 st:1,2,3> admin closed server.":         model.KindAdmin,
	} {
		if got := (Game{}).Parse(line); got.Kind != want {
			t.Errorf("Parse(%.50q) kind = %v, want %v", line, got.Kind, want)
		}
	}
}

// The date is day-first with a two-digit year and no zone, which is why Plan
// pins the container to UTC.
func TestParseReadsTheDayFirstTimestamp(t *testing.T) {
	ev := (Game{}).Parse("[26-08-26 19:14:26.045] LOG  : General      f:0 st:1,2,3> hello.")
	if ev.Drop() {
		t.Fatal("a well-formed line was dropped")
	}

	want := time.Date(2026, 8, 26, 19, 14, 26, 45_000_000, time.UTC)
	if !ev.At.Equal(want) {
		t.Errorf("At = %v, want %v", ev.At, want)
	}
}

// A stack trace is exactly the thing you want to still be able to read, so an
// unprefixed line is not classified rather than being thrown away.
func TestParseDropsOnlyWhatItCannotRead(t *testing.T) {
	for _, line := range []string{
		"",
		"Starting Project Zomboid Server...",
		"\tat java.base/java.lang.Thread.run(Thread.java:840)",
	} {
		if got := (Game{}).Parse(line); !got.Drop() {
			t.Errorf("Parse(%q) classified an unprefixed line as %v", line, got.Kind)
		}
	}
}

// ---- capabilities -------------------------------------------------------

// fakeConn is the transport, faked. The plugin only has to know what to say,
// which is the whole point of games.Conn being the caller's problem.
type fakeConn struct {
	reply string
	err   error
	sent  []string
}

func (f *fakeConn) RCON(_ context.Context, cmd string) (string, error) {
	f.sent = append(f.sent, cmd)
	if f.err != nil {
		return "", f.err
	}
	return f.reply, nil
}
func (f *fakeConn) Exec(context.Context, []string) ([]byte, error) { return nil, nil }
func (f *fakeConn) HTTP(context.Context, string, string, []byte) ([]byte, error) {
	return nil, nil
}
func (f *fakeConn) Addr() string { return "127.0.0.1:16261" }

// The capability Valheim cannot have and this game exists to prove: a roster
// that is asked for rather than reconstructed, so the name-pairing debt does
// not apply here at all.
func TestRosterAsksTheServer(t *testing.T) {
	c := &fakeConn{reply: "Players connected (2):\n-Huldra\n-Bjorn\n"}

	players, err := (Game{}).Roster(context.Background(), c)
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	if len(players) != 2 {
		t.Fatalf("got %d players, want 2: %+v", len(players), players)
	}
	if players[0].Name != "Huldra" || players[1].Name != "Bjorn" {
		t.Errorf("players = %+v", players)
	}
	if len(c.sent) != 1 || c.sent[0] != "players" {
		t.Errorf("sent %v, want [players]", c.sent)
	}
}

func TestRosterOnAnEmptyServer(t *testing.T) {
	c := &fakeConn{reply: "Players connected (0):\n"}

	players, err := (Game{}).Roster(context.Background(), c)
	if err != nil {
		t.Fatalf("Roster: %v", err)
	}
	if len(players) != 0 {
		t.Errorf("got %+v, want nobody", players)
	}
}

func TestRosterReportsAFailure(t *testing.T) {
	c := &fakeConn{err: errors.New("connection refused")}

	if _, err := (Game{}).Roster(context.Background(), c); err == nil {
		t.Error("a failed roster call reported success")
	}
}

// Several commands succeed silently, and a blank line is indistinguishable
// from a dropped connection.
func TestCommandSaysSomethingWhenTheServerDoesNot(t *testing.T) {
	c := &fakeConn{reply: "   "}

	out, err := (Game{}).Command(context.Background(), c, "save")
	if err != nil {
		t.Fatalf("Command: %v", err)
	}
	if strings.TrimSpace(out) == "" {
		t.Error("a silent success came back as a blank line")
	}
}

func TestCommandRefusesNothing(t *testing.T) {
	c := &fakeConn{reply: "ok"}
	if _, err := (Game{}).Command(context.Background(), c, "   "); err == nil {
		t.Error("an empty command was sent")
	}
	if len(c.sent) != 0 {
		t.Errorf("sent %v for an empty command", c.sent)
	}
}

// The capability drain was waiting for. servermsg needs its argument quoted or
// the server takes the first word only.
func TestWarnQuotesItsMessage(t *testing.T) {
	c := &fakeConn{reply: "ok"}

	if err := (Game{}).Warn(context.Background(), c, 15*time.Minute); err != nil {
		t.Fatalf("Warn: %v", err)
	}
	if len(c.sent) != 1 {
		t.Fatalf("sent %v", c.sent)
	}
	sent := c.sent[0]
	if !strings.HasPrefix(sent, `servermsg "`) {
		t.Errorf("sent %q, want a quoted servermsg", sent)
	}
	if !strings.Contains(sent, "15 minutes") {
		t.Errorf("sent %q, want the time in words", sent)
	}
}

func TestSaveFlushesTheWorld(t *testing.T) {
	c := &fakeConn{reply: "ok"}
	if err := (Game{}).Save(context.Background(), c); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if len(c.sent) != 1 || c.sent[0] != "save" {
		t.Errorf("sent %v, want [save]", c.sent)
	}
}

// Load order matters for this game, and a dependency listed after the mod that
// needs it is a server that will not start.
func TestModsGoIntoBothKeysInOrder(t *testing.T) {
	inst := instance()
	mods := []games.Mod{
		{ID: "2822286426", Name: "Jotunn", Enabled: true},
		{ID: "1299328280", Name: "Hydrocraft", Enabled: true},
		{ID: "9999999999", Name: "Disabled", Enabled: false},
	}

	files, err := (Game{}).Apply(inst, mods)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var ini string
	for _, f := range files {
		if strings.HasSuffix(f.Path, ".ini") {
			ini = string(f.Data)
		}
	}
	if !strings.Contains(ini, "WorkshopItems=2822286426;1299328280") {
		t.Errorf("workshop ids are missing or reordered:\n%s", grep(ini, "WorkshopItems"))
	}
	if !strings.Contains(ini, "Mods=Jotunn;Hydrocraft") {
		t.Errorf("mod names are missing or reordered:\n%s", grep(ini, "Mods="))
	}
	if strings.Contains(ini, "9999999999") {
		t.Error("a disabled mod was written into the config")
	}
}

func TestLoadOrderMatters(t *testing.T) {
	if !(Game{}).LoadOrderMatters() {
		t.Error("Project Zomboid loads mods in order and the plugin says otherwise")
	}
	if (Game{}).ModSource() != games.ModSourceWorkshop {
		t.Errorf("mod source = %v, want the Workshop", (Game{}).ModSource())
	}
}

// ---- helpers ------------------------------------------------------------

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func head(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) > 12 {
		lines = lines[:12]
	}
	return strings.Join(lines, "\n")
}

func grep(s, needle string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return "(not found)"
}
