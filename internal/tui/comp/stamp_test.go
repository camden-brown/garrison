package comp_test

import (
	"strings"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/tui/comp"
)

// chicago is a zone with a real offset and a real DST transition, which is
// what the operator machine this was found on actually runs.
var chicago = mustLoad("America/Chicago")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		// A machine without tzdata falls back to a fixed offset; the point
		// of the tests is the conversion, not the database.
		return time.FixedZone("CDT", -5*60*60)
	}
	return loc
}

func withLocal(t *testing.T, loc *time.Location, f func()) {
	t.Helper()
	saved := time.Local
	time.Local = loc
	defer func() { time.Local = saved }()
	f()
}

// The bug this was found through: a game writes its log in the container's
// clock, which Garrison pins to UTC. Rendering that without converting showed
// 12:58 to an operator whose own clock said 07:58 — and the status bar an inch
// away, which used time.Now(), showed the right one.
func TestStampConvertsToLocal(t *testing.T) {
	withLocal(t, chicago, func() {
		at := time.Date(2026, 9, 10, 12, 58, 0, 0, time.UTC)
		now := at

		got := strings.TrimSpace(comp.Stamp(at, now))
		if strings.Contains(got, "12:58") {
			t.Errorf("Stamp() = %q — still rendering UTC", got)
		}
		if !strings.Contains(got, "07:58") {
			t.Errorf("Stamp() = %q, want the local 07:58", got)
		}
	})
}

func TestStampTimeConvertsToLocal(t *testing.T) {
	withLocal(t, chicago, func() {
		at := time.Date(2026, 9, 10, 12, 58, 0, 0, time.UTC)
		if got := strings.TrimSpace(comp.StampTime(at)); got != "07:58" {
			t.Errorf("StampTime() = %q, want 07:58", got)
		}
	})
}

// Today stays bare; anything else says which day. "21:07" is unreadable on an
// event that might have been an hour ago or last Tuesday.
func TestStampSaysWhichDay(t *testing.T) {
	withLocal(t, time.UTC, func() {
		now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

		today := strings.TrimSpace(comp.Stamp(now.Add(-2*time.Hour), now))
		if today != "10:00" {
			t.Errorf("today = %q, want a bare time", today)
		}

		yesterday := strings.TrimSpace(comp.Stamp(now.AddDate(0, 0, -1), now))
		if !strings.HasPrefix(yesterday, "Wed") {
			t.Errorf("yesterday = %q, want a weekday", yesterday)
		}

		old := strings.TrimSpace(comp.Stamp(now.AddDate(0, 0, -30), now))
		if !strings.Contains(old, "Aug") {
			t.Errorf("a month ago = %q, want a date", old)
		}
	})
}

// Ten minutes either side of midnight is eighteen minutes and two days, and
// "yesterday" is the more useful answer than "less than a day ago".
func TestStampCountsCalendarDaysNotHours(t *testing.T) {
	withLocal(t, time.UTC, func() {
		now := time.Date(2026, 9, 10, 0, 10, 0, 0, time.UTC)
		at := time.Date(2026, 9, 9, 23, 50, 0, 0, time.UTC)

		got := strings.TrimSpace(comp.Stamp(at, now))
		if got == "23:50" {
			t.Errorf("Stamp() = %q — twenty minutes across midnight was treated as today", got)
		}
	})
}

// Every stamp occupies the same cells, or a column of them is ragged.
func TestStampIsAFixedWidth(t *testing.T) {
	withLocal(t, chicago, func() {
		now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
		for _, at := range []time.Time{
			now,
			now.AddDate(0, 0, -1),
			now.AddDate(0, 0, -30),
			now.AddDate(0, 0, 1), // a clock that went backwards
			{},                   // never happened
		} {
			if w := comp.Width(comp.Stamp(at, now)); w != comp.StampWidth {
				t.Errorf("Stamp(%v) is %d cells, want %d", at, w, comp.StampWidth)
			}
		}
		if w := comp.Width(comp.StampTime(time.Time{})); w != comp.TimeWidth {
			t.Errorf("StampTime(zero) is %d cells, want %d", w, comp.TimeWidth)
		}
	})
}

// An event with no timestamp is a real thing — a line the game wrote without
// one — and it must not render as midnight on the first of January.
func TestAZeroTimeIsNotAnHour(t *testing.T) {
	withLocal(t, chicago, func() {
		now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
		if got := comp.Stamp(time.Time{}, now); strings.Contains(got, ":") {
			t.Errorf("Stamp(zero) = %q, want no time at all", got)
		}
		if got := comp.StampTime(time.Time{}); strings.Contains(got, ":") {
			t.Errorf("StampTime(zero) = %q, want no time at all", got)
		}
	})
}

func TestSameDayIsLocal(t *testing.T) {
	withLocal(t, chicago, func() {
		// 02:00 UTC and 23:00 UTC the day before are the same local day in
		// Chicago, which is the comparison a day separator has to make.
		a := time.Date(2026, 9, 10, 2, 0, 0, 0, time.UTC)
		b := time.Date(2026, 9, 9, 23, 0, 0, 0, time.UTC)

		if !comp.SameDay(a, b) {
			t.Error("two instants on the same local day were called different days")
		}
		if comp.SameDay(a, a.AddDate(0, 0, -1)) {
			t.Error("instants a day apart were called the same day")
		}
		if comp.SameDay(time.Time{}, a) {
			t.Error("a zero time was called the same day as a real one")
		}
	})
}

func TestStampDayNamesTodayAndYesterday(t *testing.T) {
	withLocal(t, time.UTC, func() {
		now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

		if got := comp.StampDay(now, now); !strings.HasPrefix(got, "Today") {
			t.Errorf("StampDay(today) = %q", got)
		}
		if got := comp.StampDay(now.AddDate(0, 0, -1), now); !strings.HasPrefix(got, "Yesterday") {
			t.Errorf("StampDay(yesterday) = %q", got)
		}
		if got := comp.StampDay(now.AddDate(0, 0, -5), now); strings.Contains(got, "Today") {
			t.Errorf("StampDay(last week) = %q, want a real day", got)
		}
		if got := comp.StampDay(time.Time{}, now); got != "" {
			t.Errorf("StampDay(zero) = %q, want empty", got)
		}
	})
}
