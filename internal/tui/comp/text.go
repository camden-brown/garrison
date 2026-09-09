package comp

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// Truncate cuts a string to a display width, appending an ellipsis when it had
// to cut.
//
// Two things it has to get right, and both are invisible until they are not.
//
// Width, not length: a rune is not a cell. CJK and emoji occupy two columns,
// and slicing by byte or by rune shears the grid one row at a time until the
// whole table is crooked.
//
// And escape sequences are not content. A styled string carries colour codes
// that occupy no cells, so counting them as width truncates far too early —
// and cutting through the middle of one leaves its tail on screen as literal
// text, which is where a stray "[38;5;8m" beside a column heading comes from.
// ansi.Truncate understands both; runewidth understands only the first.
func Truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if Width(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return ansi.Truncate(s, width, "…")
}

// Width is a string's display width, ignoring any escape sequences in it.
func Width(s string) int { return ansi.StringWidth(s) }

// Pad right-pads a string to a display width, truncating if it is too long, so
// a column always occupies exactly the cells it claims.
func Pad(s string, width int) string {
	s = Truncate(s, width)
	if gap := width - Width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

// PadLeft is Pad for right-aligned columns: numbers, mostly.
func PadLeft(s string, width int) string {
	s = Truncate(s, width)
	if gap := width - Width(s); gap > 0 {
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

// Bytes renders a byte count the way an operator reads one.
func Bytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}

	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return strconv.FormatFloat(float64(n)/float64(div), 'f', 1, 64) + " " + string("KMGT"[exp]) + "iB"
}
