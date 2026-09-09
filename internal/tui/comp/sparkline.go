// Package comp holds the drawing pieces more than one view needs.
//
// A component earns its place here when a second view wants it. Until then it
// lives in the view that uses it — two views drawing the same table
// differently is the smell that says promote, not a rule to apply in advance.
package comp

import (
	"strings"

	"github.com/camden-brown/garrison/internal/model"
)

// blocks are the eight lower-block elements, U+2581 to U+2588.
//
// Block elements, never Braille. Braille gives four times the horizontal
// resolution and is the obvious choice right up until a monospace font on
// Windows lacks the glyphs: the fallback is a different width, every cell
// after it shifts, and the whole table shears. These eight are present in
// every font Windows Terminal ships with.
var blocks = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// asciiBlocks is the --ascii fallback. It is worse to read and it is honest
// about the shape, which is the trade --ascii exists to make.
var asciiBlocks = []rune{'_', '.', '.', '-', '-', '=', '=', '#'}

// Sparkline renders points as a fixed-width bar strip.
//
// It always returns exactly width cells, because a sparkline is a table column
// and a column that sometimes comes up short drags everything after it
// leftward on that row only.
type Sparkline struct {
	Width int
	ASCII bool

	// Min and Max pin the vertical scale. Leave Max zero to scale to the
	// data instead, which is the right default when there is no natural
	// ceiling — memory has one (the container limit) and network throughput
	// does not.
	Min, Max float64
}

// Render draws the newest Width points, oldest at the left.
func (s Sparkline) Render(points []model.Point) string {
	if s.Width <= 0 {
		return ""
	}

	glyphs := blocks
	if s.ASCII {
		glyphs = asciiBlocks
	}
	// Newest Width points. Older ones are off the left edge of the strip.
	if len(points) > s.Width {
		points = points[len(points)-s.Width:]
	}

	lo, hi := s.scale(points)

	var b strings.Builder
	b.Grow(s.Width * 3)

	// Left-pad so the newest sample is always hard against the right edge.
	// A strip that fills from the left makes a server that just started
	// look like one whose history has been truncated.
	for i := 0; i < s.Width-len(points); i++ {
		b.WriteRune(' ')
	}

	for _, p := range points {
		b.WriteRune(glyphs[bucket(p.Mean, lo, hi, len(glyphs))])
	}
	return b.String()
}

// scale decides the vertical range.
//
// An explicit Max wins — memory has a real ceiling and should be drawn against
// it. Otherwise the range comes from the data's own minimum and maximum, so
// the strip shows the shape of the variation rather than its magnitude. That
// is the conventional sparkline trade and it is the right one here because the
// current value is printed directly above the strip: the number carries the
// magnitude, the strip carries the shape.
//
// The case that forced this: network throughput steady at 52 KiB/s scaled from
// zero draws a solid block of full-height bars, which reads as a pegged meter
// when it means the opposite. Scaled to its own range it is a flat line, which
// is what "steady" should look like.
func (s Sparkline) scale(points []model.Point) (lo, hi float64) {
	if s.Max > s.Min {
		return s.Min, s.Max
	}
	if len(points) == 0 {
		return 0, 1
	}

	lo, hi = points[0].Mean, points[0].Mean
	for _, p := range points {
		if p.Mean < lo {
			lo = p.Mean
		}
		if p.Mean > hi {
			hi = p.Mean
		}
	}
	if hi <= lo {
		// No variation at all. Give it a range so the strip draws a flat
		// line along the bottom rather than dividing by zero.
		return lo, lo + 1
	}
	return lo, hi
}

// bucket maps a value onto one of n glyphs.
func bucket(v, lo, hi float64, n int) int {
	if hi <= lo {
		return 0
	}
	if v <= lo {
		return 0
	}
	if v >= hi {
		return n - 1
	}

	i := int((v - lo) / (hi - lo) * float64(n))
	if i >= n {
		i = n - 1
	}
	if i < 0 {
		i = 0
	}
	return i
}
