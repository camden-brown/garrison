package comp_test

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/camden-brown/garrison/internal/tui/comp"
)

func typed(f comp.Filter, text string) comp.Filter {
	for _, r := range text {
		f, _ = f.Key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return f
}

func TestFilterTypesAndMatches(t *testing.T) {
	f := typed(comp.Filter{}.Open(), "val")

	if !f.On() {
		t.Error("a filter with text in it is not on")
	}
	if !f.Matches("valheim-huldra") {
		t.Error("the query does not match a row containing it")
	}
	if f.Matches("zomboid-main") {
		t.Error("the query matches a row that does not contain it")
	}
}

// An empty filter hides nothing. Getting this backwards empties every screen
// the moment the bar is opened.
func TestAnEmptyFilterMatchesEverything(t *testing.T) {
	var f comp.Filter
	if !f.Matches("anything") || f.On() {
		t.Error("an empty filter is filtering")
	}
}

func TestFilterIsCaseInsensitive(t *testing.T) {
	f := typed(comp.Filter{}.Open(), "VAL")
	if !f.Matches("valheim-huldra") {
		t.Error("filtering is case sensitive")
	}
}

func TestFilterMatchesAnyField(t *testing.T) {
	f := typed(comp.Filter{}.Open(), "crash")
	if !f.Matches("valheim-huldra", "valheim", "crashed") {
		t.Error("a match in a later field was missed")
	}
}

// Enter accepts: the bar closes and the rows stay filtered. A filter that
// forgets itself when you stop typing is a search box.
func TestEnterKeepsTheQuery(t *testing.T) {
	f := typed(comp.Filter{}.Open(), "val")
	f, _ = f.Key(tea.KeyMsg{Type: tea.KeyEnter})

	if f.Active {
		t.Error("enter left the bar open")
	}
	if !f.On() || !f.Matches("valheim") {
		t.Error("enter discarded the query")
	}
}

// Escape is the way back to everything, which is why it clears rather than
// merely closing.
func TestEscapeClearsTheQuery(t *testing.T) {
	f := typed(comp.Filter{}.Open(), "val")
	f, _ = f.Key(tea.KeyMsg{Type: tea.KeyEsc})

	if f.Active || f.On() {
		t.Error("escape did not clear the filter")
	}
	if !f.Matches("zomboid") {
		t.Error("the cleared filter is still filtering")
	}
}

// While open it swallows everything, or a view's own keys fire mid-word.
func TestAnOpenFilterConsumesEveryKey(t *testing.T) {
	f := comp.Filter{}.Open()
	for _, msg := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{'j'}},
		{Type: tea.KeyRunes, Runes: []rune{'S'}},
		{Type: tea.KeyUp},
	} {
		var handled bool
		if f, handled = f.Key(msg); !handled {
			t.Errorf("%v was not consumed by an open filter", msg)
		}
	}
}

func TestAClosedFilterConsumesNothing(t *testing.T) {
	var f comp.Filter
	if _, handled := f.Key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}); handled {
		t.Error("a closed filter consumed a key")
	}
}

// Reopening keeps what is there, so "/" twice is a correction.
func TestReopeningKeepsTheQuery(t *testing.T) {
	f := typed(comp.Filter{}.Open(), "val")
	f, _ = f.Key(tea.KeyMsg{Type: tea.KeyEnter})
	f = f.Open()

	if f.Query != "val" {
		t.Errorf("query = %q after reopening, want it kept", f.Query)
	}
	if f.Caret != 3 {
		t.Errorf("caret = %d, want it at the end", f.Caret)
	}
}

// A closed filter that is still filtering has to say so, or missing rows look
// like missing servers.
func TestAClosedButActiveFilterStillRenders(t *testing.T) {
	f := typed(comp.Filter{}.Open(), "val")
	f, _ = f.Key(tea.KeyMsg{Type: tea.KeyEnter})

	got := f.Render(comp.NewTheme(false), 40)
	if !strings.Contains(got, "val") {
		t.Errorf("render = %q, want it to show the query still in force", got)
	}
}

func TestAnUnusedFilterRendersNothing(t *testing.T) {
	var f comp.Filter
	if got := f.Render(comp.NewTheme(false), 40); got != "" {
		t.Errorf("render = %q, want nothing", got)
	}
}

func TestFilterRenderFitsItsWidth(t *testing.T) {
	f := typed(comp.Filter{}.Open(), strings.Repeat("long", 30))
	for _, width := range []int{10, 40, 92} {
		if w := comp.Width(f.Render(comp.NewTheme(false), width)); w > width {
			t.Errorf("width %d: render is %d cells", width, w)
		}
	}
}
