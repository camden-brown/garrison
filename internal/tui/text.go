package tui

import (
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
)

// Truncate cuts a string to a display width, appending an ellipsis when it had
// to cut.
//
// Width, not length. A rune is not a cell: CJK and emoji occupy two columns,
// and slicing by byte or by rune shears the grid one row at a time until the
// whole table is crooked. go-runewidth is the only thing here that knows the
// difference.
func Truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return runewidth.Truncate(s, width, "…")
}

// Pad right-pads a string to a display width, truncating if it is too long, so
// a column always occupies exactly the cells it claims.
func Pad(s string, width int) string {
	s = Truncate(s, width)
	if gap := width - runewidth.StringWidth(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

// PadLeft is Pad for right-aligned columns: numbers, mostly.
func PadLeft(s string, width int) string {
	s = Truncate(s, width)
	if gap := width - runewidth.StringWidth(s); gap > 0 {
		return strings.Repeat(" ", gap) + s
	}
	return s
}

// Duration renders an uptime the way an operator reads one: two units at most,
// largest first. "6d 04h" tells you more than "148h13m22s" and fits a column.
func Duration(d time.Duration) string {
	if d <= 0 {
		return "—"
	}

	switch {
	case d < time.Minute:
		return itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + "m"
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		return itoa(h) + "h " + pad2(m) + "m"
	default:
		days := int(d.Hours()) / 24
		h := int(d.Hours()) % 24
		return itoa(days) + "d " + pad2(h) + "h"
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func pad2(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}
