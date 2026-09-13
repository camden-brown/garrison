package main

import (
	"reflect"
	"testing"

	"github.com/camden-brown/garrison/internal/games"
	"github.com/camden-brown/garrison/internal/model"
	"github.com/camden-brown/garrison/internal/services/mods"

	_ "github.com/camden-brown/garrison/internal/games/all"
)

// The two layout types are the same fact on either side of the dependency
// rule: a service cannot import internal/games, so cmd copies the fields
// across. Copying fields by hand is exactly the kind of thing that gets
// forgotten when one grows — and it was, the day Config was added, which sent
// a patcher's configuration nowhere and left it hooking the wrong assembly.
//
// Comparing the field sets is the cheapest way to make forgetting loud.
func TestEveryLayoutFieldIsCarriedAcross(t *testing.T) {
	from := reflect.TypeOf(games.ModLayout{})
	to := reflect.TypeOf(mods.Layout{})

	if from.NumField() != to.NumField() {
		t.Fatalf("games.ModLayout has %d fields and mods.Layout has %d; the copy in modLayout cannot be complete",
			from.NumField(), to.NumField())
	}
	for i := 0; i < from.NumField(); i++ {
		if from.Field(i).Name != to.Field(i).Name {
			t.Errorf("field %d is %q on one side and %q on the other", i, from.Field(i).Name, to.Field(i).Name)
		}
	}

	// And the copy itself: every field a real game fills in arrives.
	g, err := games.Get("valheim")
	if err != nil {
		t.Fatalf("valheim is not registered: %v", err)
	}
	installable, ok := g.(games.Installable)
	if !ok {
		t.Fatal("valheim no longer installs mods, so this test is testing nothing")
	}

	inst := model.Instance{Name: "a", Game: "valheim", Data: "/data"}
	src := reflect.ValueOf(installable.ModLayout(inst))
	got := reflect.ValueOf(modLayout(inst, installable))
	for i := 0; i < src.NumField(); i++ {
		want := src.Field(i).String()
		if want == "" {
			continue
		}
		if got.Field(i).String() != want {
			t.Errorf("%s = %q, want %q — modLayout dropped it",
				from.Field(i).Name, got.Field(i).String(), want)
		}
	}
}
