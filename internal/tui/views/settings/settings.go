// Package settings renders a game's settings as a form.
//
// There is no game-specific code here and there must never be any. A plugin
// returns typed fields; the groups, the editors, the validation, the help, the
// impact badges and the apply all derive from them. Adding a setting to a game
// is one Field in its Schema and nothing else — that is the whole trade, and
// the moment this file contains a switch on a game id the trade is off.
package settings

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/camden-brown/garrison/internal/core"
	"github.com/camden-brown/garrison/internal/games"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/tui"
	"github.com/camden-brown/garrison/internal/tui/comp"
)

// pane is which half of the screen the keys are driving.
//
// Two independent cursors is the point at which DESIGN says a view is two
// views wearing one hat. The fix it prescribes is a sub-pane with its own
// focus rather than a bigger Update with a mode flag, which is this.
type pane uint8

const (
	paneGroups pane = iota
	paneFields
)

// View is the settings form.
type View struct {
	pane  pane
	group int
	field int

	// confirming is set while an apply is waiting for a yes.
	confirming bool

	// editing is set while a text field has the keyboard, with cursor the
	// caret position within it.
	//
	// Both are cursor-shaped state, which is the only kind a view is allowed
	// to hold. The text itself goes straight into the store's draft on every
	// keystroke, so rebuilding this view on a resize costs a caret position
	// nobody will notice and never a half-typed value.
	editing bool
	cursor  int

	// advanced reveals the fields a plugin marked as such, which are hidden
	// by default to keep the common form short.
	advanced bool
}

func New() *View { return &View{} }

func (v *View) ID() tui.ViewID { return tui.ViewSettings }
func (v *View) Title() string  { return "Settings" }

// Available refuses when the game has no settings to show. The view answers
// this itself, so no game knowledge leaks into the shell.
func (v *View) Available(inst model.Instance) (bool, string) {
	if inst.Name == "" {
		return false, "Pick a server first — settings belong to one."
	}
	g, err := games.Get(inst.Game)
	if err != nil {
		return false, fmt.Sprintf("No plugin for %q, so Garrison does not know its settings.", inst.Game)
	}
	if len(g.Schema().Fields) == 0 {
		return false, g.Meta().Name + " has no settings to configure."
	}
	return true, ""
}

var (
	keyUp       = key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up"))
	keyDown     = key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down"))
	keyLeft     = key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "groups"))
	keyRight    = key.NewBinding(key.WithKeys("right", "l", "enter"), key.WithHelp("→/l", "fields"))
	keyDec      = key.NewBinding(key.WithKeys("-", "_"), key.WithHelp("-", "lower"))
	keyInc      = key.NewBinding(key.WithKeys("+", "="), key.WithHelp("+", "raise"))
	keyToggle   = key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "toggle"))
	keyEdit     = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "edit text"))
	keyStopEdit = key.NewBinding(key.WithKeys("enter", "esc"), key.WithHelp("enter/esc", "done"))
	keyAdvanced = key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "advanced"))
	keyReset    = key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reset field"))
	keyDiscard  = key.NewBinding(key.WithKeys("D"), key.WithHelp("D", "discard all"))
	keyApply    = key.NewBinding(key.WithKeys("A"), key.WithHelp("A", "apply"))
	keyYes      = key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "confirm"))
	keyNo       = key.NewBinding(key.WithKeys("n", "esc"), key.WithHelp("n/esc", "cancel"))
)

func (v *View) Keys() []key.Binding {
	return []key.Binding{keyUp, keyDown, keyLeft, keyRight, keyEdit, keyToggle, keyInc, keyDec, keyAdvanced, keyReset, keyApply}
}

func (v *View) Update(msg tea.Msg, f tui.Frame, snap core.Snapshot) (tui.View, tea.Cmd) {
	msgKey, ok := msg.(tea.KeyMsg)
	if !ok {
		return v, nil
	}

	srv, form, ok := v.form(f, snap)
	if !ok {
		return v, nil
	}
	next := *v

	// A pending apply swallows every other key, the same way the fleet's
	// stop confirmation does.
	if next.confirming {
		switch {
		case key.Matches(msgKey, keyYes):
			next.confirming = false
			return &next, tui.Apply(srv.Name, needsRecreate(srv, form))
		case key.Matches(msgKey, keyNo):
			next.confirming = false
		}
		return &next, nil
	}

	// Text editing swallows every other key while it is on, the same way the
	// confirmation above does — otherwise typing "a" into a server name
	// would toggle the advanced fields and "D" would discard the form.
	if next.editing {
		field, ok := next.focused(form)
		if !ok || !editable(field) {
			next.editing = false
			return &next, nil
		}
		if key.Matches(msgKey, keyStopEdit) {
			next.editing = false
			return &next, nil
		}

		// Every keystroke goes to the store. There is no local buffer to
		// lose and no separate commit: the draft is the buffer, which is
		// why esc leaves what was typed rather than reverting it. Backing
		// out of a change is r for this field or D for the form, the same
		// two keys that back out of a toggle.
		value := stringValue(srv, field)
		out, cursor, handled := comp.EditKey(value, next.cursor, msgKey)
		if !handled {
			return &next, nil
		}
		next.cursor = cursor
		if out != value {
			return &next, tui.Edit(srv.Name, field.Key, out)
		}
		return &next, nil
	}

	// Enter on a focused text field starts editing it, checked ahead of the
	// pane keys below because enter also means "move into the fields pane"
	// and by this point we are already in it.
	if field, ok := next.focused(form); ok && editable(field) && key.Matches(msgKey, keyEdit) {
		next.editing = true
		next.cursor = len([]rune(stringValue(srv, field)))
		return &next, nil
	}

	switch {
	case key.Matches(msgKey, keyApply):
		if srv.Pending() == 0 {
			return &next, nil
		}
		// Anything that costs more than a file write is confirmed, because
		// the operator pressed a key on a form and may not have read the
		// footer that says what it does.
		if needsRecreate(srv, form) || worstImpact(srv, form) >= games.ImpactRestart {
			next.confirming = true
			return &next, nil
		}
		return &next, tui.Apply(srv.Name, false)

	case key.Matches(msgKey, keyAdvanced):
		next.advanced = !next.advanced
		next.clamp(form)
		return &next, nil

	case key.Matches(msgKey, keyLeft):
		next.pane = paneGroups
		return &next, nil

	case key.Matches(msgKey, keyRight):
		next.pane = paneFields
		return &next, nil

	case key.Matches(msgKey, keyUp):
		next.move(-1, form)
		return &next, nil

	case key.Matches(msgKey, keyDown):
		next.move(+1, form)
		return &next, nil

	case key.Matches(msgKey, keyDiscard):
		return &next, tui.Discard(srv.Name)
	}

	// The rest act on the focused field.
	fields := form.fields(next.group, next.advanced)
	if next.pane != paneFields || len(fields) == 0 {
		return &next, nil
	}
	field := fields[min(next.field, len(fields)-1)]

	switch {
	case key.Matches(msgKey, keyToggle):
		if field.Type == games.TypeBool {
			return &next, tui.Edit(srv.Name, field.Key, !boolOf(srv, field))
		}
		if field.Type == games.TypeEnum {
			return &next, tui.Edit(srv.Name, field.Key, nextOption(srv, field, +1))
		}
	case key.Matches(msgKey, keyInc):
		if v, ok := step(srv, field, +1); ok {
			return &next, tui.Edit(srv.Name, field.Key, v)
		}
	case key.Matches(msgKey, keyDec):
		if v, ok := step(srv, field, -1); ok {
			return &next, tui.Edit(srv.Name, field.Key, v)
		}
	case key.Matches(msgKey, keyReset):
		return &next, tui.Edit(srv.Name, field.Key, field.Default)
	}
	return &next, nil
}

func (v *View) move(delta int, form form) {
	if v.pane == paneGroups {
		v.group += delta
		v.field = 0
	} else {
		v.field += delta
	}
	v.clamp(form)
}

func (v *View) clamp(form form) {
	if v.group >= len(form.groups) {
		v.group = len(form.groups) - 1
	}
	if v.group < 0 {
		v.group = 0
	}

	n := len(form.fields(v.group, v.advanced))
	if v.field >= n {
		v.field = n - 1
	}
	if v.field < 0 {
		v.field = 0
	}
}

// focused is the field the keys act on, when the fields pane has them.
func (v *View) focused(form form) (games.Field, bool) {
	if v.pane != paneFields {
		return games.Field{}, false
	}
	fields := form.fields(v.group, v.advanced)
	if len(fields) == 0 {
		return games.Field{}, false
	}
	return fields[min(v.field, len(fields)-1)], true
}

// editable reports whether a field takes typed text.
//
// TypeSecret is deliberately not editable. The form has nowhere to put a
// password except the server's TOML file, which is the thing games.TypeSecret
// says it will not do — so until there is somewhere better, a password is
// changed in the file by someone who knows they are writing it there. See
// ADR 0009.
func editable(field games.Field) bool { return field.Type == games.TypeString }

// stringValue is a field's current value as text: the draft if it has been
// edited, the saved setting otherwise, and the default when it has neither.
func stringValue(srv core.Server, field games.Field) string {
	v, ok := srv.Setting(field.Key)
	if !ok || v == nil {
		v = field.Default
	}
	s, _ := v.(string)
	return s
}

// worstImpact is the most expensive thing among the pending changes, which is
// what applying them actually costs.
func worstImpact(srv core.Server, form form) games.Impact {
	keys := make([]string, 0, len(srv.Draft))
	for key := range srv.Draft {
		if srv.Edited(key) {
			keys = append(keys, key)
		}
	}
	return form.schema.MaxImpact(keys)
}

// needsRecreate reports whether the pending changes require a new container.
//
// This is the one place that decides, and it decides from the plugin's own
// Schema rather than from anything this package knows about the game — which
// is what lets Zomboid arrive at M3 without touching the apply path.
func needsRecreate(srv core.Server, form form) bool {
	return worstImpact(srv, form) >= games.ImpactRecreate
}

// form is a game's schema arranged the way the screen shows it.
type form struct {
	schema games.Schema
	groups []string
}

func (f form) fields(group int, advanced bool) []games.Field {
	if group < 0 || group >= len(f.groups) {
		return nil
	}
	name := f.groups[group]

	out := make([]games.Field, 0, len(f.schema.Fields))
	for _, field := range f.schema.Fields {
		if field.Group != name {
			continue
		}
		if field.Advanced && !advanced {
			continue
		}
		out = append(out, field)
	}
	return out
}

// hasAdvanced reports whether hiding advanced fields is hiding anything, so
// the hint only appears when it is true.
func (f form) hasAdvanced() bool {
	for _, field := range f.schema.Fields {
		if field.Advanced {
			return true
		}
	}
	return false
}

func (v *View) form(f tui.Frame, snap core.Snapshot) (core.Server, form, bool) {
	srv, ok := snap.Server(f.Server)
	if !ok {
		return core.Server{}, form{}, false
	}
	g, err := games.Get(srv.Game)
	if err != nil {
		return srv, form{}, false
	}
	schema := g.Schema()
	return srv, form{schema: schema, groups: schema.Groups()}, true
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func boolOf(srv core.Server, field games.Field) bool {
	if v, ok := srv.Setting(field.Key); ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	b, _ := field.Default.(bool)
	return b
}

// nextOption cycles an enum. Wrapping rather than stopping, because an enum
// with three choices should not need the arrow keys learned in two directions.
func nextOption(srv core.Server, field games.Field, delta int) any {
	if len(field.Options) == 0 {
		return field.Default
	}

	current, _ := srv.Setting(field.Key)
	at := 0
	for i, o := range field.Options {
		if o.Value == current {
			at = i
			break
		}
	}
	return field.Options[((at+delta)%len(field.Options)+len(field.Options))%len(field.Options)].Value
}

// step raises or lowers a numeric field, clamped to the range the plugin
// declared. Validation lives in the schema, so this cannot produce a value the
// game will reject.
func step(srv core.Server, field games.Field, delta int) (any, bool) {
	current, _ := srv.Setting(field.Key)

	switch field.Type {
	case games.TypeInt:
		n, ok := intOf(current, field.Default)
		if !ok {
			return nil, false
		}
		n += int64(delta)
		if lo, ok := intBound(field.Min); ok && n < lo {
			n = lo
		}
		if hi, ok := intBound(field.Max); ok && n > hi {
			n = hi
		}
		return n, true

	case games.TypeFloat:
		n, ok := floatOf(current, field.Default)
		if !ok {
			return nil, false
		}
		n += float64(delta) * 0.1
		if lo, ok := floatBound(field.Min); ok && n < lo {
			n = lo
		}
		if hi, ok := floatBound(field.Max); ok && n > hi {
			n = hi
		}
		return n, true

	case games.TypeEnum:
		return nextOption(srv, field, delta), true
	}
	return nil, false
}

func intOf(v, fallback any) (int64, bool) {
	if n, ok := toInt(v); ok {
		return n, true
	}
	return toInt(fallback)
}

func floatOf(v, fallback any) (float64, bool) {
	if n, ok := toFloat(v); ok {
		return n, true
	}
	return toFloat(fallback)
}

func toInt(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	}
	return 0, false
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float32:
		return float64(n), true
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func intBound(v any) (int64, bool)     { return toInt(v) }
func floatBound(v any) (float64, bool) { return toFloat(v) }

// display renders a value the way the form shows it.
func display(srv core.Server, field games.Field) string {
	v, ok := srv.Setting(field.Key)
	if !ok || v == nil {
		v = field.Default
	}

	switch field.Type {
	case games.TypeBool:
		if b, _ := v.(bool); b {
			return "on"
		}
		return "off"

	case games.TypeSecret:
		// Never rendered and never written to a config file. Showing the
		// length would leak more than nothing does.
		if v == nil || v == "" {
			return "not set"
		}
		return "••••••••"

	case games.TypeEnum:
		for _, o := range field.Options {
			if o.Value == v {
				return o.Label
			}
		}
	}

	if v == nil {
		return "—"
	}
	if s, ok := v.(string); ok {
		if s == "" {
			return "—"
		}
		return s
	}
	if f, ok := v.(float64); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// badge names what applying a change costs. Live changes get none: a badge on
// everything is a badge on nothing.
func badge(t *comp.Theme, impact games.Impact) string {
	switch impact {
	case games.ImpactRestart:
		return t.Accent.Render("restart")
	case games.ImpactRecreate:
		return t.Bar.Render("recreate")
	case games.ImpactWipeRisk:
		return t.Err.Render("WIPE RISK")
	}
	return ""
}

func (v *View) Render(f tui.Frame, snap core.Snapshot) string {
	t := f.Theme

	srv, form, ok := v.form(f, snap)
	if !ok {
		return t.Dim.Render("Pick a server to see its settings.") + "\n"
	}
	if available, why := v.Available(srv.Instance); !available && srv.Configured {
		return t.Dim.Render(why) + "\n"
	}
	v.clamp(form)

	groupWidth := 18
	fieldWidth := f.Width - groupWidth - 1

	body := comp.Columns(1,
		comp.Panel{
			Theme: t, Title: "GROUPS", Width: groupWidth,
			Focused: f.Focused && v.pane == paneGroups,
		}.Render(v.groupList(t, form)),
		comp.Panel{
			Theme: t, Title: strings.ToUpper(form.groups[v.group]), Right: v.hint(srv, form),
			Width:   fieldWidth,
			Focused: f.Focused && v.pane == paneFields,
		}.Render(v.fieldList(t, srv, form, comp.Inner(fieldWidth))),
	)

	return body + "\n" + v.footer(f, srv, form)
}

func (v *View) groupList(t *comp.Theme, form form) string {
	inner := comp.Inner(18)

	var b strings.Builder
	for i, name := range form.groups {
		selected := i == v.group
		marker := "  "
		if selected {
			marker = "▌ "
			if t.ASCII {
				marker = "> "
			}
		}
		style := t.Dim
		if selected {
			style = t.Title
		}
		b.WriteString(t.On(t.Accent, selected).Render(marker))
		b.WriteString(t.On(style, selected).Render(comp.Pad(name, inner-2)))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (v *View) fieldList(t *comp.Theme, srv core.Server, form form, width int) string {
	fields := form.fields(v.group, v.advanced)
	if len(fields) == 0 {
		return t.Dim.Render("Nothing in this group.")
	}

	labelWidth := 24
	valueWidth := 18

	var b strings.Builder
	for i, field := range fields {
		selected := v.pane == paneFields && i == v.field

		marker := "  "
		if selected {
			marker = "▌ "
			if t.ASCII {
				marker = "> "
			}
		}

		label := comp.Pad(field.Label, labelWidth)
		value := comp.Pad(display(srv, field), valueWidth)

		valueStyle := t.Title
		if srv.Edited(field.Key) {
			// An edited value is amber, and a dot marks the row, so a
			// change is findable without comparing against memory.
			valueStyle = t.Accent
			label = comp.Pad("• "+field.Label, labelWidth)
		}

		b.WriteString(t.On(t.Accent, selected).Render(marker))
		b.WriteString(t.On(t.Dim, selected).Render(label))
		if selected && v.editing {
			// The input paints its own caret, so it goes down unwrapped:
			// a style around it would reset the caret's colours mid-string
			// and leave the escape codes on screen.
			b.WriteString(comp.Input{
				Value:  stringValue(srv, field),
				Cursor: v.cursor,
				Width:  valueWidth,
				Theme:  t,
			}.Render())
		} else {
			b.WriteString(t.On(valueStyle, selected).Render(value))
		}
		b.WriteString(t.On(t.Dim, selected).Render(comp.Pad("  "+badgeText(field.Impact), width-2-labelWidth-valueWidth)))
		b.WriteString("\n")

		if selected && field.Help != "" {
			// Help for the focused field only, always visible. Two lines at
			// most, so the form does not reflow as the cursor moves.
			b.WriteString(t.Dim.Render(comp.Truncate("    "+field.Help, width)))
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// badgeText is the plain form, for width arithmetic. The coloured one is
// applied by the caller so a selected row keeps its background.
func badgeText(impact games.Impact) string {
	switch impact {
	case games.ImpactRestart:
		return "restart"
	case games.ImpactRecreate:
		return "recreate"
	case games.ImpactWipeRisk:
		return "WIPE RISK"
	}
	return ""
}

func (v *View) hint(srv core.Server, form form) string {
	if v.editing {
		return "typing · enter/esc done"
	}
	if field, ok := v.focused(form); ok && editable(field) {
		return "enter edit · r reset"
	}
	if form.hasAdvanced() {
		if v.advanced {
			return "a hide advanced"
		}
		return "a advanced"
	}
	return "space toggle · ± adjust"
}

// footer says what applying would cost, which is the question the form exists
// to answer before anything is written.
func (v *View) footer(f tui.Frame, srv core.Server, form form) string {
	t := f.Theme

	pending := srv.Pending()
	if pending == 0 {
		return t.Dim.Render("No unapplied changes.")
	}

	if v.confirming {
		return t.Accent.Render(comp.Truncate(
			fmt.Sprintf("apply %d change%s to %s?  %s  ·  y / n",
				pending, plural(pending), srv.Name, applyCost(worstImpact(srv, form))), f.Width))
	}

	keys := make([]string, 0, pending)
	for key := range srv.Draft {
		if srv.Edited(key) {
			keys = append(keys, key)
		}
	}

	worst := form.schema.MaxImpact(keys)
	line := t.Accent.Render(fmt.Sprintf("%d unapplied change%s", pending, plural(pending)))
	line += t.Dim.Render("  ·  applying will ")

	switch worst {
	case games.ImpactLive:
		line += t.Dim.Render("take effect immediately")
	case games.ImpactRestart:
		line += t.Accent.Render("restart the server")
	case games.ImpactRecreate:
		line += t.Bar.Render("recreate the container")
	case games.ImpactWipeRisk:
		line += t.Err.Render("RISK THE WORLD — a backup is taken first")
	}
	line += t.Dim.Render("  ·  A apply  ·  D discard")
	return comp.Truncate(line, f.Width)
}

// applyCost is the plain sentence, for the confirmation.
func applyCost(impact games.Impact) string {
	switch impact {
	case games.ImpactRestart:
		return "the server will be restarted"
	case games.ImpactRecreate:
		return "the container will be recreated; the world is untouched"
	case games.ImpactWipeRisk:
		return "THIS CAN DESTROY THE WORLD — a backup is taken first"
	}
	return "it takes effect immediately"
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
