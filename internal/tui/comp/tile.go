package comp

import (
	"strings"

	"github.com/camden-brown/garrison/internal/model"
)

// TileWidth is the outer width of one tile, border included.
//
// Fixed rather than proportional so a row of them lines up with the table
// below and with itself at any terminal size. Sized so four fit beside the
// rail at 120 columns, which is the width the design is drawn at: the stage
// gets 92, and 4×22 plus three single-column gaps is 91.
const TileWidth = 22

// Tile is one headline number: a label, the figure, and a sparkline of how it
// got there.
type Tile struct {
	Label string
	// Right is the small note at the far end of the heading — the window a
	// sparkline covers, or the capacity a figure is a fraction of.
	Right  string
	Value  string
	Note   string
	Points []model.Point

	// Min and Max pin the sparkline's scale. Left zero it scales to the
	// data and shows shape instead of magnitude, which is right for a
	// series with no natural ceiling and wrong for one that has a real one.
	Min, Max float64

	// Accent overrides the value's colour, for a figure that is itself a
	// warning — an attention count that is not zero.
	Accent bool
}

// TileStrip renders tiles in a row of boxes, wrapping when the width runs out.
func TileStrip(t *Theme, width int, tiles []Tile) string {
	perRow := (width + 1) / (TileWidth + 1)
	if perRow < 1 {
		perRow = 1
	}

	var rows []string
	for start := 0; start < len(tiles); start += perRow {
		end := start + perRow
		if end > len(tiles) {
			end = len(tiles)
		}

		boxes := make([]string, 0, end-start)
		for _, tile := range tiles[start:end] {
			boxes = append(boxes, tile.render(t))
		}
		rows = append(rows, Columns(1, boxes...))
	}
	return strings.Join(rows, "\n")
}

// InlineTiles is the under-30-rows layout: one line of values, no boxes and no
// sparklines. A real narrow layout rather than a clipped wide one.
func InlineTiles(t *Theme, width int, tiles []Tile) string {
	parts := make([]string, 0, len(tiles))
	for _, tile := range tiles {
		parts = append(parts, t.Header.Render(tile.Label)+" "+t.Title.Render(tile.Value))
	}
	return Truncate(strings.Join(parts, "  ·  "), width)
}

func (tile Tile) render(t *Theme) string {
	inner := TileWidth - 2

	value := t.Title.Render(Pad(tile.Value, inner))
	if tile.Accent {
		value = t.Accent.Render(Pad(tile.Value, inner))
	}

	spark := Sparkline{Width: inner, ASCII: t.ASCII, Min: tile.Min, Max: tile.Max}.Render(tile.Points)

	body := strings.Join([]string{
		value,
		t.Bar.Render(spark),
		t.Dim.Render(Pad(tile.Note, inner)),
	}, "\n")

	return Panel{Theme: t, Title: tile.Label, Right: tile.Right, Width: TileWidth}.Render(body)
}
