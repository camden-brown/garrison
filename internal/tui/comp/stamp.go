package comp

import "time"

// StampWidth is the fixed width every timestamp column occupies, so rows line
// up whether or not they carry a date.
const StampWidth = 12

// Stamp renders an instant the way an operator reads one: in their own
// timezone, and saying which day when that is not obvious.
//
// Two things it fixes, and both were real.
//
// **Timezone.** A game writes its log in the container's clock, which Garrison
// pins to UTC so that parsing is unambiguous (see valheim.Plan). Rendering that
// instant without converting shows UTC — which on a machine five hours off
// means the activity feed and the status bar disagree by five hours while both
// look plausible. Every timestamp a person reads goes through here, and here
// converts.
//
// **The day.** "21:07" is unreadable on anything but today's events: a server
// that crashed at 21:07 is a different story depending on whether that was an
// hour ago or last Tuesday. Today stays bare because that is the common case
// and the noise is not worth it; anything else says which day.
//
// now is passed rather than read so a render stays a pure function of its
// inputs and a golden file does not move at midnight.
func Stamp(at, now time.Time) string {
	if at.IsZero() {
		return PadLeft("—", StampWidth)
	}

	local, ref := at.Local(), now.Local()

	// Compare calendar days rather than subtracting durations: 23:50 and
	// 00:10 are eighteen minutes and two different days, and "yesterday" is
	// the more useful answer.
	days := daysBetween(local, ref)

	switch {
	case days == 0:
		return PadLeft(local.Format("15:04"), StampWidth)
	case days > 0 && days < 7:
		// Within the week the weekday is what people actually reason with.
		return PadLeft(local.Format("Mon 15:04"), StampWidth)
	default:
		// Older, or somehow in the future — a clock that went backwards
		// should still render rather than claim a weekday that misleads.
		return PadLeft(local.Format("02 Jan 15:04"), StampWidth)
	}
}

// TimeWidth is the width of a bare time-of-day column.
const TimeWidth = 5

// StampTime is the local time of day and nothing else.
//
// For a list that already says which day — a console under a day separator —
// repeating the date on every row is noise. It still converts to local, which
// is the part that was wrong.
func StampTime(at time.Time) string {
	if at.IsZero() {
		return PadLeft("—", TimeWidth)
	}
	return PadLeft(at.Local().Format("15:04"), TimeWidth)
}

// StampDay is the full day, for a separator row between days.
func StampDay(at, now time.Time) string {
	if at.IsZero() {
		return ""
	}
	local, ref := at.Local(), now.Local()

	switch daysBetween(local, ref) {
	case 0:
		return "Today · " + local.Format("Mon 2 Jan")
	case 1:
		return "Yesterday · " + local.Format("Mon 2 Jan")
	}
	return local.Format("Monday 2 January")
}

// SameDay reports whether two instants fall on the same local calendar day,
// which is what a day separator keys off.
func SameDay(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return false
	}
	la, lb := a.Local(), b.Local()
	ya, ma, da := la.Date()
	yb, mb, db := lb.Date()
	return ya == yb && ma == mb && da == db
}

// daysBetween is how many calendar days back `at` is from `ref`, negative for
// the future.
func daysBetween(at, ref time.Time) int {
	y1, m1, d1 := at.Date()
	y2, m2, d2 := ref.Date()

	// Midnight-to-midnight in the local zone, so a DST transition inside the
	// interval does not turn a day into 23 hours and round the wrong way.
	a := time.Date(y1, m1, d1, 0, 0, 0, 0, at.Location())
	b := time.Date(y2, m2, d2, 0, 0, 0, 0, ref.Location())
	return int(b.Sub(a).Hours() / 24)
}
