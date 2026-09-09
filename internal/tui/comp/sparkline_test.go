package comp

import (
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/camden-brown/garrison/internal/model"
)

func points(values ...float64) []model.Point {
	out := make([]model.Point, len(values))
	base := time.Date(2026, 9, 9, 21, 0, 0, 0, time.UTC)
	for i, v := range values {
		out[i] = model.Point{At: base.Add(time.Duration(i) * time.Second), Mean: v, Min: v, Max: v}
	}
	return out
}

// A sparkline is a table column. One that sometimes comes up short drags
// everything after it leftward on that row only, which reads as corruption.
func TestAlwaysExactlyWidthCells(t *testing.T) {
	tests := []struct {
		name  string
		width int
		pts   []model.Point
	}{
		{"empty", 10, nil},
		{"fewer points than width", 10, points(1, 2, 3)},
		{"exactly width", 5, points(1, 2, 3, 4, 5)},
		{"more points than width", 5, points(1, 2, 3, 4, 5, 6, 7, 8, 9)},
		{"all zero", 8, points(0, 0, 0, 0)},
		{"identical values", 8, points(7, 7, 7, 7, 7, 7, 7, 7)},
		{"width one", 1, points(1, 2, 3)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, ascii := range []bool{false, true} {
				got := Sparkline{Width: tt.width, ASCII: ascii}.Render(tt.pts)
				if n := runewidth.StringWidth(got); n != tt.width {
					t.Errorf("ascii=%v: %d cells, want %d (%q)", ascii, n, tt.width, got)
				}
			}
		})
	}
}

// Block elements are single-width in every font Windows Terminal ships. A
// double-width glyph would shear the whole grid one row at a time.
func TestEveryGlyphIsSingleWidth(t *testing.T) {
	for _, set := range [][]rune{blocks, asciiBlocks} {
		for _, r := range set {
			if w := runewidth.RuneWidth(r); w != 1 {
				t.Errorf("%q is %d cells wide, want 1", r, w)
			}
		}
	}
}

// Braille is the tempting alternative and the reason this test exists: its
// coverage in monospace fonts is unreliable, and a fallback glyph of a
// different width shears the cell grid.
func TestNoBrailleGlyphs(t *testing.T) {
	for _, r := range blocks {
		if r >= 0x2800 && r <= 0x28FF {
			t.Errorf("%q is Braille", r)
		}
		if r < 0x2580 || r > 0x259F {
			t.Errorf("%q is outside the block-elements range U+2580–U+259F", r)
		}
	}
}

// The newest sample is the one you look at, so it sits against the right edge.
// Filling from the left makes a server that started a minute ago look like one
// whose history was truncated.
func TestShortHistoryIsRightAligned(t *testing.T) {
	got := Sparkline{Width: 8}.Render(points(1, 9))

	if !strings.HasPrefix(got, "      ") {
		t.Errorf("render = %q, want it padded on the left", got)
	}
	if strings.HasSuffix(got, " ") {
		t.Errorf("render = %q, want the newest sample at the right edge", got)
	}
}

func TestOnlyTheNewestPointsAreDrawn(t *testing.T) {
	// A rising ramp: if the oldest were kept the strip would start high.
	got := Sparkline{Width: 3, Min: 0, Max: 9}.Render(points(9, 9, 9, 1, 2, 3))

	if strings.ContainsRune(got, '█') {
		t.Errorf("render = %q, want only the last three low values", got)
	}
}

func TestExplicitScalePinsTheRange(t *testing.T) {
	// 0 and 100 against a 0–100 scale must hit the bottom and top glyphs.
	got := Sparkline{Width: 2, Min: 0, Max: 100}.Render(points(0, 100))

	if []rune(got)[0] != blocks[0] {
		t.Errorf("first glyph = %q, want the lowest block", []rune(got)[0])
	}
	if []rune(got)[1] != blocks[len(blocks)-1] {
		t.Errorf("last glyph = %q, want the highest block", []rune(got)[1])
	}
}

// An idle server should look idle. Auto-scaling a flat zero line to fill the
// strip is how a dashboard cries wolf.
func TestFlatSeriesDoesNotFillTheStrip(t *testing.T) {
	got := Sparkline{Width: 6}.Render(points(0, 0, 0, 0, 0, 0))

	for _, r := range got {
		if r == blocks[len(blocks)-1] {
			t.Errorf("render = %q, want a flat line at the bottom, not a full bar", got)
		}
	}
}

func TestAutoScaleUsesTheData(t *testing.T) {
	got := Sparkline{Width: 3}.Render(points(0, 25, 50))

	runes := []rune(got)
	if runes[0] >= runes[2] {
		t.Errorf("render = %q, want a rising ramp", got)
	}
}

func TestZeroWidthRendersNothing(t *testing.T) {
	if got := (Sparkline{Width: 0}).Render(points(1, 2, 3)); got != "" {
		t.Errorf("render = %q, want empty", got)
	}
}

func TestBucket(t *testing.T) {
	tests := []struct {
		v, lo, hi float64
		want      int
	}{
		{0, 0, 100, 0},
		{100, 0, 100, 7},
		{50, 0, 100, 4},
		{-5, 0, 100, 0},  // below the floor clamps
		{500, 0, 100, 7}, // above the ceiling clamps
		{1, 1, 1, 0},     // degenerate range
	}

	for _, tt := range tests {
		if got := bucket(tt.v, tt.lo, tt.hi, 8); got != tt.want {
			t.Errorf("bucket(%v, %v, %v) = %d, want %d", tt.v, tt.lo, tt.hi, got, tt.want)
		}
	}
}

// A steady non-zero series is the case that forced auto-scale to use the
// data's own range: scaled from zero it draws a solid block, which reads as a
// pegged meter when it means the exact opposite.
func TestSteadyNonZeroSeriesReadsAsFlat(t *testing.T) {
	got := Sparkline{Width: 6}.Render(points(52000, 52000, 52000, 52000, 52000, 52000))

	for _, r := range got {
		if r == blocks[len(blocks)-1] {
			t.Errorf("render = %q, want a flat line rather than a solid bar", got)
		}
	}
}

// The trade that buys: small variation is visible instead of being flattened
// against an irrelevant ceiling.
func TestSmallVariationIsVisible(t *testing.T) {
	got := Sparkline{Width: 4}.Render(points(50.0, 50.1, 50.2, 50.3))

	runes := []rune(got)
	if runes[0] == runes[3] {
		t.Errorf("render = %q, want the rise to be visible", got)
	}
}

// An explicit ceiling still wins, because memory has a real one.
func TestExplicitMaxBeatsTheDataRange(t *testing.T) {
	// Half of an 8-unit ceiling must not draw as full height just because
	// every sample is identical.
	got := Sparkline{Width: 4, Min: 0, Max: 8}.Render(points(4, 4, 4, 4))

	for _, r := range got {
		if r == blocks[len(blocks)-1] {
			t.Errorf("render = %q, want mid-height against the explicit ceiling", got)
		}
		if r == blocks[0] {
			t.Errorf("render = %q, want mid-height, not the floor", got)
		}
	}
}
