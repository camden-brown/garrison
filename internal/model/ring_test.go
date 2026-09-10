package model_test

import (
	"strconv"
	"testing"

	"github.com/camden-brown/garrison/internal/model"
)

func fill(r model.Ring, n int) model.Ring {
	for i := 0; i < n; i++ {
		r = r.Add(model.Event{Kind: model.KindInfo, Text: strconv.Itoa(i)})
	}
	return r
}

func TestRingKeepsEverythingUnderTheBound(t *testing.T) {
	r := fill(model.NewRing(8), 5)

	if got := r.Len(); got != 5 {
		t.Errorf("Len() = %d, want 5", got)
	}
	if got := r.Dropped(); got != 0 {
		t.Errorf("Dropped() = %d, want 0", got)
	}
	if got := r.Events()[0].Text; got != "0" {
		t.Errorf("first event = %q, want %q", got, "0")
	}
}

func TestRingBoundsAndDropsTheOldest(t *testing.T) {
	const cap, added = 8, 100
	r := fill(model.NewRing(cap), added)

	if got := r.Len(); got != cap {
		t.Errorf("Len() = %d, want %d", got, cap)
	}
	if got := r.Added(); got != added {
		t.Errorf("Added() = %d, want %d", got, added)
	}
	if got := r.Dropped(); got != added-cap {
		t.Errorf("Dropped() = %d, want %d", got, added-cap)
	}

	events := r.Events()
	if got := events[len(events)-1].Text; got != strconv.Itoa(added-1) {
		t.Errorf("newest = %q, want %q", got, strconv.Itoa(added-1))
	}
	if got := events[0].Text; got != strconv.Itoa(added-cap) {
		t.Errorf("oldest = %q, want %q", got, strconv.Itoa(added-cap))
	}
}

// The whole reason this type exists rather than a copy-on-append slice: an
// older snapshot must keep reading what it was given, whatever the writer has
// done since. This is the test that fails if the sharing is ever wrong.
func TestRingSnapshotsAreUnaffectedByLaterWrites(t *testing.T) {
	const cap = 16

	early := fill(model.NewRing(cap), 4)
	earlyEvents := early.Events()

	// Enough to force several compactions.
	later := early
	for i := 0; i < cap*10; i++ {
		later = later.Add(model.Event{Text: "later"})
	}

	if got := early.Len(); got != 4 {
		t.Errorf("the early snapshot grew to %d, want 4", got)
	}
	for i, ev := range earlyEvents {
		if want := strconv.Itoa(i); ev.Text != want {
			t.Errorf("the early snapshot's event %d is now %q, want %q", i, ev.Text, want)
		}
	}
	if got := later.Len(); got != cap {
		t.Errorf("the later ring holds %d, want %d", got, cap)
	}
}

func TestRingTail(t *testing.T) {
	r := fill(model.NewRing(64), 50)

	tail := r.Tail(3)
	if len(tail) != 3 {
		t.Fatalf("Tail(3) returned %d events", len(tail))
	}
	if got := tail[0].Text; got != "47" {
		t.Errorf("Tail(3)[0] = %q, want %q", got, "47")
	}

	if got := len(r.Tail(500)); got != 50 {
		t.Errorf("Tail(500) returned %d, want everything (50)", got)
	}
	if got := r.Tail(0); got != nil {
		t.Errorf("Tail(0) = %v, want nil", got)
	}
}

// A zero Ring is what a server starts with, so it has to work without anyone
// having called NewRing.
func TestZeroRingIsUsable(t *testing.T) {
	var r model.Ring

	if r.Len() != 0 || len(r.Events()) != 0 {
		t.Fatalf("a zero ring is not empty: len=%d", r.Len())
	}
	r = r.Add(model.Event{Text: "first"})
	if r.Len() != 1 {
		t.Errorf("Len() = %d after one Add, want 1", r.Len())
	}
	if got := r.Events()[0].Text; got != "first" {
		t.Errorf("Events()[0] = %q, want %q", got, "first")
	}
}

func TestRingDefaultsToConsoleCap(t *testing.T) {
	r := model.NewRing(0).Add(model.Event{Text: "x"})
	for i := 0; i < 10; i++ {
		r = r.Add(model.Event{Text: "y"})
	}
	if got := r.Len(); got != 11 {
		t.Errorf("Len() = %d, want 11 — nothing should be dropped at the default bound", got)
	}
}
