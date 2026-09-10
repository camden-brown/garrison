package tui

import (
	"context"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tui/comp"
)

// Store is what the shell needs from internal/core. It is an interface so the
// app can be driven by a stub in a test without standing up a writer goroutine.
type Store interface {
	Subscribe() <-chan core.Snapshot
	Snapshot() core.Snapshot
	Start(ctx context.Context, instance string)
	Stop(ctx context.Context, instance string)
	Restart(ctx context.Context, instance string)
	Backup(ctx context.Context, instance string)
	Restore(ctx context.Context, instance, archive string)
	Update(ctx context.Context, instance string)
	ApplySettings(ctx context.Context, instance string, recreate bool)
	EditSetting(ctx context.Context, instance, key string, value any)
	DiscardDraft(ctx context.Context, instance string)
	CancelTask(ctx context.Context, id string)

	// CreateServer writes a new server's configuration. The wizard is the
	// only caller, and it is here rather than behind a message because it
	// answers immediately — there is no task to watch.
	CreateServer(ctx context.Context, inst model.Instance)
	DeleteServer(ctx context.Context, instance string)

	// Notify is how the shell reports a command line that made no sense.
	// It is the store's because a notice outlives the keystroke that
	// caused it — the fleet's attention pane shows the same list.
	Notify(ctx context.Context, server, text string)
}

// railThreshold is the width below which the rail is dropped.
const railThreshold = 100

// App is the shell: the rail, the stage, and the status bar.
//
// It owns navigation — which server, which view, where the keys go — and
// nothing else. Every screen is behind the View interface, so adding one does
// not touch this file.
type App struct {
	store  Store
	ctx    context.Context
	views  []View
	active int

	// selected is the instance the rail points at, empty for the fleet
	// entry. It lives here rather than in each view because the rail and
	// the stage show the same selection, and two cursors that can disagree
	// about which server you are looking at is a bug waiting for a busy
	// evening.
	selected string
	focus    comp.Focus

	// The command line: ":" opens it, and it takes the verbs that are
	// otherwise keys. It lives on the shell rather than in a view because
	// its verbs are fleet-wide — ":stop zomboid-main" should work from the
	// Backups screen — and because a view that owned it would have to be
	// asked to give the keyboard back.
	//
	// The text is shell state for the same reason the Backups confirmation
	// is view state: an abandoned half-typed command is not worth
	// preserving across a resize, and the shell is rebuilt far less often
	// than a view.
	commanding bool
	command    string
	caret      int

	// The palette: ctrl+P over servers, views and verbs.
	palette       bool
	paletteQuery  string
	paletteCaret  int
	paletteCursor int

	// wizard is the "n" provisioner form.
	wizard wizardState

	// helping is the "?" overlay.
	helping bool

	// deleting is the server whose delete confirmation is up, with the name
	// being typed into it. Like the Backups restore prompt this is view
	// state on purpose: a confirmation lost to a resize is a destructive
	// action that did not happen.
	deleting    string
	deleteTyped string
	deleteCaret int

	// sharing holds the text "y" copied, shown until a key dismisses it.
	// It is not a copy of state: it is a record of what left the process,
	// which is the only way to check that what was pasted is what was on
	// screen.
	sharing string

	// ambient is DESIGN's F: no rail, no status bar, a card per server.
	// Any key leaves, which is why it is checked before every other
	// binding — a mode you have to remember the exit key for is a mode
	// somebody force-quits the terminal out of.
	ambient bool

	snap   core.Snapshot
	sub    <-chan core.Snapshot
	theme  *comp.Theme
	width  int
	height int

	// Now is the clock, exported so a golden render can pin it. The status
	// bar shows a time, and a golden that moves every minute is a golden
	// nobody trusts.
	Now func() time.Time
}

// NewApp builds the shell over a store and a set of views.
func NewApp(ctx context.Context, store Store, theme *comp.Theme, views ...View) *App {
	return &App{
		store: store,
		ctx:   ctx,
		views: views,
		theme: theme,
		snap:  store.Snapshot(),
		focus: comp.FocusStage,
		// A sane size before the first WindowSizeMsg arrives, so the very
		// first frame is not rendered into a zero-width terminal.
		width:  120,
		height: 34,
		Now:    time.Now,
	}
}

func (a *App) clock() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *App) Init() tea.Cmd {
	a.sub = a.store.Subscribe()
	return waitForSnapshot(a.sub)
}

// snapshotMsg carries a published snapshot into the Bubble Tea loop. The store
// publishes on its own goroutine and the UI receives here, which is the only
// place the two meet.
type snapshotMsg struct {
	snap core.Snapshot
	ok   bool
}

func waitForSnapshot(sub <-chan core.Snapshot) tea.Cmd {
	return func() tea.Msg {
		snap, ok := <-sub
		return snapshotMsg{snap: snap, ok: ok}
	}
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		return a, nil

	case snapshotMsg:
		if !msg.ok {
			// The store shut down. Nothing will publish again, so stop
			// rather than spin on a closed channel.
			return a, tea.Quit
		}
		a.snap = msg.snap
		a.pruneSelection()
		return a, waitForSnapshot(a.sub)

	case ActionMsg:
		return a, a.dispatch(msg)

	case SelectMsg:
		a.selected = msg.Server
		return a, nil

	case EditMsg:
		a.store.EditSetting(a.ctx, msg.Server, msg.Key, msg.Value)
		return a, nil

	case DiscardMsg:
		a.store.DiscardDraft(a.ctx, msg.Server)
		return a, nil

	case CancelTaskMsg:
		a.store.CancelTask(a.ctx, msg.ID)
		return a, nil

	case tea.KeyMsg:
		return a.key(msg)
	}

	return a.routeToView(msg)
}

// pruneSelection drops a selection whose server has gone. The fleet changes
// every five seconds and a rail pointing at a container that no longer exists
// would send every subsequent action nowhere.
func (a *App) pruneSelection() {
	if a.selected == "" {
		return
	}
	if _, ok := a.snap.Server(a.selected); !ok {
		a.selected = ""
	}
}

func (a *App) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// The command line swallows everything while it is open, including q.
	if a.commanding {
		return a.commandKey(msg)
	}

	// The palette, like the command line, swallows everything while open.
	if a.palette {
		return a.paletteKey(msg)
	}
	if a.wizard.open {
		return a.wizardKey(msg)
	}
	if a.deleting != "" {
		return a.deleteKey(msg)
	}

	// The share panel is a receipt, not a mode: any key clears it and is
	// otherwise handled normally, so it never gets in the way.
	if a.sharing != "" {
		a.sharing = ""
	}

	// Help is a mode, and the only way out is any key — which is what the
	// panel says.
	if a.helping {
		a.helping = false
		if msg.String() == "ctrl+c" {
			return a, tea.Quit
		}
		return a, nil
	}

	// Any key leaves ambient mode, ctrl+c included — it quits as well, so
	// the one key that always works still always works.
	if a.ambient {
		a.ambient = false
		if k := msg.String(); k == "ctrl+c" {
			return a, tea.Quit
		}
		return a, nil
	}

	switch k := msg.String(); k {
	case "ctrl+c", "q":
		return a, tea.Quit

	case ":":
		a.commanding, a.command, a.caret = true, "", 0
		return a, nil

	case "ctrl+p":
		a.palette, a.paletteQuery, a.paletteCaret, a.paletteCursor = true, "", 0, 0
		return a, nil

	case "tab":
		// Three stops, always in the same order, so the cycle is
		// predictable without looking: which server, which view, the view.
		a.focus = (a.focus + 1) % 3
		return a, nil

	case "f":
		a.selected = ""
		a.show(ViewFleet)
		return a, nil

	case "F":
		a.ambient = true
		return a, nil

	case "n":
		a.openWizard()
		return a, nil

	case "y":
		return a, a.share()

	case "?":
		a.helping = true
		return a, nil

	case "X":
		a.startDelete()
		return a, nil

	case "[":
		a.stepServer(-1)
		return a, nil

	case "]":
		a.stepServer(+1)
		return a, nil

	case "1", "2", "3", "4", "5", "6", "7":
		// The number keys jump to a view of the current server and never
		// change meaning, so they are an index into the registry rather
		// than a lookup by id.
		n, _ := strconv.Atoi(k)
		if n < len(a.views) {
			a.active = n
			a.ensureServerSelected()
		}
		return a, nil
	}

	// Arrow keys belong to whichever list has focus.
	if a.focus == comp.FocusServers {
		if cmd, handled := a.moveServer(msg); handled {
			return a, cmd
		}
	}
	if a.focus == comp.FocusViews {
		if handled := a.moveView(msg); handled {
			return a, nil
		}
	}
	return a.routeToView(msg)
}

// ensureServerSelected picks one when a per-server view is opened from the
// fleet entry, so the screen has something to be about.
func (a *App) ensureServerSelected() {
	if a.selected != "" || len(a.snap.Servers) == 0 {
		return
	}
	a.selected = a.snap.Servers[0].Name
}

func (a *App) moveServer(msg tea.KeyMsg) (tea.Cmd, bool) {
	delta := 0
	switch msg.String() {
	case "up", "k":
		delta = -1
	case "down", "j":
		delta = 1
	default:
		return nil, false
	}

	// The fleet entry sits above the servers as index -1, so moving up from
	// the first server lands on "All servers" rather than sticking.
	i := -1
	for n, srv := range a.snap.Servers {
		if srv.Name == a.selected {
			i = n
			break
		}
	}

	i += delta
	if i < -1 {
		i = -1
	}
	if i >= len(a.snap.Servers) {
		i = len(a.snap.Servers) - 1
	}
	if i < 0 {
		a.selected = ""
		a.show(ViewFleet)
		return nil, true
	}
	a.selected = a.snap.Servers[i].Name
	return nil, true
}

func (a *App) moveView(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "up", "k":
		if a.active > 0 {
			a.active--
		}
	case "down", "j":
		if a.active < len(a.views)-1 {
			a.active++
		}
	default:
		return false
	}
	a.ensureServerSelected()
	return true
}

// show switches to a view by id. Unknown ids are ignored rather than
// panicking: the keymap and the registry are edited separately and drifting
// apart should not take the program down.
func (a *App) show(id ViewID) {
	for i, v := range a.views {
		if v.ID() == id {
			a.active = i
			return
		}
	}
}

func (a *App) routeToView(msg tea.Msg) (tea.Model, tea.Cmd) {
	if len(a.views) == 0 {
		return a, nil
	}
	next, cmd := a.views[a.active].Update(msg, a.frame(), a.snap)
	a.views[a.active] = next
	return a, cmd
}

// frame is what the active view is told about its surroundings. Update and
// Render get the same one, so a view never has to remember anything.
func (a *App) frame() Frame {
	width := a.width
	if a.width >= railThreshold {
		width = a.width - comp.RailWidth - 2
	}
	return Frame{
		Width:   width,
		Height:  a.height - 2,
		Theme:   a.theme,
		Now:     a.clock(),
		Server:  a.selected,
		Focused: a.focus == comp.FocusStage,
	}
}

// dispatch turns a view's request into work. This is the only place the shell
// touches the store's write side, and it never blocks: the store records the
// operation and reports back as a snapshot.
func (a *App) dispatch(msg ActionMsg) tea.Cmd {
	switch msg.Op {
	case core.OpStart:
		a.store.Start(a.ctx, msg.Server)
	case core.OpStop:
		a.store.Stop(a.ctx, msg.Server)
	case core.OpRestart:
		a.store.Restart(a.ctx, msg.Server)
	case core.OpBackup:
		a.store.Backup(a.ctx, msg.Server)
	case core.OpRestore:
		a.store.Restore(a.ctx, msg.Server, msg.Archive)
	case core.OpDelete:
		a.store.DeleteServer(a.ctx, msg.Server)
	case core.OpUpdate:
		a.store.Update(a.ctx, msg.Server)
	case core.OpApply:
		a.store.ApplySettings(a.ctx, msg.Server, msg.Recreate)
	}
	return nil
}

func (a *App) View() string {
	if len(a.views) == 0 {
		return "no views registered\n"
	}

	// Ambient mode is the whole terminal: no rail, no status bar, nothing
	// but the cards. Dropping the bar is the point rather than an oversight
	// — it is the row that makes a dashboard look like a tool rather than
	// something you leave on a second monitor.
	if a.ambient {
		return a.ambientView()
	}

	// One status bar, and one blank line above it. The command line, when
	// it is open, takes a row from the stage rather than overlaying it:
	// covering the row you are acting on is how you act on the wrong one.
	body := a.height - 2
	if a.commanding {
		body--
	}
	// The palette is an overlay in spirit and a panel in fact: it takes the
	// rows it needs from the stage rather than drawing over them, for the
	// same reason the command line does.
	paletteHeight := 0
	if a.palette {
		paletteHeight = paletteRows + 4
		body -= paletteHeight
		if body < 3 {
			body = 3
		}
	}
	if a.wizard.open {
		body -= wizardRows
		if body < 3 {
			body = 3
		}
	}
	if a.sharing != "" {
		body -= shareRows
		if body < 3 {
			body = 3
		}
	}
	if a.helping {
		body -= helpRows
		if body < 3 {
			body = 3
		}
	}
	if a.deleting != "" {
		body -= deleteRows
		if body < 3 {
			body = 3
		}
	}

	// Under 100 columns the rail collapses and navigation moves entirely to
	// the keys, per DESIGN §3. A 26-column rail beside a 70-column terminal
	// leaves the stage too narrow to hold the fleet table, and every view
	// has a real narrow layout rather than a clipped wide one.
	rail, stageWidth := "", a.width
	if a.width >= railThreshold {
		rail = a.rail(body)
		stageWidth = a.width - comp.RailWidth - 2
	}

	f := a.frame()
	f.Width, f.Height = stageWidth, body
	stage := a.views[a.active].Render(f, a.snap)

	view := stage
	if rail != "" {
		view = lipgloss.JoinHorizontal(lipgloss.Top, rail, "  ", clamp(stage, stageWidth, body))
	}
	// Joining pads the shorter column to match the taller one, which leaves
	// trailing spaces on most rows. They are invisible until somebody drags
	// a selection across the terminal and copies a block of whitespace.
	out := trimRight(view)
	if a.palette {
		out += "\n" + a.paletteView(a.width)
	}
	if a.wizard.open {
		out += "\n" + a.wizardView(a.width)
	}
	if a.sharing != "" {
		out += "\n" + a.shareView(a.width)
	}
	if a.helping {
		out += "\n" + a.helpView(a.width)
	}
	if a.deleting != "" {
		out += "\n" + a.deleteView(a.width)
	}
	if line := a.commandLine(a.width); line != "" {
		out += "\n" + line
	}
	return out + "\n" + a.statusBar()
}

func (a *App) rail(height int) string {
	entries := make([]comp.RailEntry, 0, len(a.views))
	inst := a.instance()

	for i, v := range a.views {
		if v.ID() == ViewFleet {
			continue // the fleet has its own entry above the server list
		}
		_, reason := v.Available(inst)
		entries = append(entries, comp.RailEntry{
			Title:  v.Title(),
			Key:    strconv.Itoa(i),
			Reason: reason,
		})
	}

	// The fleet has its own entry above the server list rather than a line
	// in the view list, so nothing in the view list is current while it is
	// showing. Highlighting Dashboard there would claim you are somewhere
	// you are not.
	active := a.active - 1

	return comp.Rail{
		Theme:    a.theme,
		Height:   height,
		Servers:  a.snap.Servers,
		Selected: a.selected,
		Entries:  entries,
		Active:   active,
		Focus:    a.focus,
	}.Render()
}

// instance is the selected server as a model.Instance, which is what a view's
// Available takes. Until the config file lands, name and game are all Garrison
// knows — both come off the container's labels.
func (a *App) instance() model.Instance {
	srv, ok := a.snap.Server(a.selected)
	if !ok {
		return model.Instance{}
	}
	return model.Instance{Name: srv.Name, Game: srv.Game}
}

// trimRight removes trailing spaces from every line.
func trimRight(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
}

// clamp trims a rendered stage to its box, so a view that draws one line too
// many cannot push the status bar off the bottom.
func clamp(s string, width, height int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, line := range lines {
		if lipgloss.Width(line) > width {
			lines[i] = comp.Truncate(line, width)
		}
	}
	return strings.Join(lines, "\n")
}

// statusBar never changes position.
//
// It is the one thing on screen whose location you can rely on, so it carries
// the facts you look for without reading: which screen, how the fleet is
// doing, whether anything needs you, and how fresh any of it is.
func (a *App) statusBar() string {
	t := a.theme
	up, down, unknown := a.snap.Counts()

	label := "FLEET"
	if len(a.views) > 0 {
		label = strings.ToUpper(a.views[a.active].Title())
	}

	// Dark text on an accent block. The screen name is the one thing you
	// should be able to find without looking for it.
	left := []string{t.Badge.Render(" " + label + " ")}

	counts := []string{t.Dim.Render(strconv.Itoa(len(a.snap.Servers)) + " servers")}
	if up > 0 {
		counts = append(counts, t.StateStyle(model.StateRunning).Render(strconv.Itoa(up)+" up"))
	}
	if down > 0 {
		counts = append(counts, t.Dim.Render(strconv.Itoa(down)+" down"))
	}
	if unknown > 0 {
		counts = append(counts, t.StateStyle(model.StateUnknown).Render(strconv.Itoa(unknown)+" unknown"))
	}
	left = append(left, " "+strings.Join(counts, t.Rule.Render(" · ")))

	// Alerts are amber and last, because a count you only notice when it is
	// non-zero is a count that has to sit where the eye stops.
	if n := alertCount(a.snap); n > 0 {
		left = append(left, "  "+t.Accent.Render(strconv.Itoa(n)+" "+plural(n, "alert", "alerts")))
	}

	right := strings.Join([]string{
		t.Dim.Render(a.freshness()),
		t.Dim.Render("? help"),
		t.Dim.Render("Ctrl+P palette"),
		t.Dim.Render(a.clock().Format("15:04")),
	}, "  ")

	leftText := strings.Join(left, "")

	// Width, not len: both halves already carry escape sequences.
	gap := a.width - comp.Width(leftText) - comp.Width(right)
	if gap < 1 {
		gap = 1
	}
	return leftText + strings.Repeat(" ", gap) + right
}

// freshness says how current the screen is. "live 5s" means the fleet is
// re-read that often; anything else means what you are reading is stale and
// the bar should not pretend otherwise.
func (a *App) freshness() string {
	if !a.snap.Engine.OK {
		return "stale · " + a.engineWordPlain()
	}
	if a.snap.Engine.Poll > 0 {
		return "live " + core.Budget(a.snap.Engine.Poll)
	}
	return "live"
}

func alertCount(snap core.Snapshot) int {
	n := 0
	for _, srv := range snap.Servers {
		if srv.State == model.StateCrashed {
			n++
		}
	}
	for _, notice := range snap.Notices {
		if notice.Level != core.LevelInfo {
			n++
		}
	}
	return n
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// focusHint says what Tab will do next, because a three-way cycle is not
// guessable from looking at it.
func focusHint(f comp.Focus) string {
	switch f {
	case comp.FocusServers:
		return "tab → views"
	case comp.FocusViews:
		return "tab → stage"
	}
	return "tab → servers"
}

// engineWordPlain is the engine's state without styling, for embedding in a
// line that is styled as a whole.
func (a *App) engineWordPlain() string {
	transport := a.snap.Engine.Transport
	if transport == "" {
		transport = "docker"
	}
	if a.snap.Engine.OK {
		return transport
	}
	return transport + " unreachable"
}
