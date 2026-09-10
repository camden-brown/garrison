package model

// ConsoleCap is how many console lines a server keeps.
//
// Four hours of a chatty server, which is the window in which somebody asks
// "what happened?" — DESIGN's figure for the Console screen. It is a count
// rather than a duration because a crash loop produces an hour of history in
// a minute and a quiet night produces none.
const ConsoleCap = 16384

// Ring is a bounded log of events, oldest first.
//
// The obvious implementation — copy the whole slice on every append, the way
// model.History does for its 300-point tiers — is what kept the console at a
// 200-line tail: sixteen thousand events copied per log line, on a server that
// can emit thousands a second, is a render loop spent in memmove.
//
// So this one shares its backing array between snapshots and appends in place.
// That is safe here and would not be in general. It rests on the store having
// exactly one writer (ADR 0004): mutations are applied in sequence, so every
// Add operates on the newest Ring and writes at an index no older snapshot can
// address. An older snapshot holds a shorter len and keeps reading the prefix
// it always saw. Two Rings appending at the same index would corrupt both,
// which is precisely what the single writer makes impossible.
//
// The cost of the bound is paid once per Cap appends rather than on each one:
// when the backing array fills, the newest Cap events are copied into a fresh
// one and the old array is left to whichever snapshots still reference it.
type Ring struct {
	// buf holds up to 2*cap events. Everything past the newest cap has
	// scrolled off and is invisible to Events, but stays addressable so the
	// compaction happens on a schedule rather than per line.
	buf []Event
	cap int

	// added counts everything ever recorded, including what has scrolled
	// off, so a view can say what it is not showing.
	added int64
}

// NewRing returns a ring bounded to n events, or ConsoleCap when n < 1.
func NewRing(n int) Ring {
	if n < 1 {
		n = ConsoleCap
	}
	return Ring{cap: n}
}

// Add records an event and returns the updated ring.
func (r Ring) Add(ev Event) Ring {
	if r.cap < 1 {
		r.cap = ConsoleCap
	}
	if r.buf == nil {
		r.buf = make([]Event, 0, 2*r.cap)
	}
	if len(r.buf) == cap(r.buf) {
		// Full. Keep the newest cap in a new array; snapshots still
		// pointing at the old one are unaffected by anything after this.
		next := make([]Event, r.cap, 2*r.cap)
		copy(next, r.buf[len(r.buf)-r.cap:])
		r.buf = next
	}
	r.buf = append(r.buf, ev)
	r.added++
	return r
}

// Len is how many events are visible.
func (r Ring) Len() int {
	if len(r.buf) < r.cap {
		return len(r.buf)
	}
	return r.cap
}

// Added is everything ever recorded, including what has scrolled off.
func (r Ring) Added() int64 { return r.added }

// Dropped is how many events have scrolled out of the window.
func (r Ring) Dropped() int64 { return r.added - int64(r.Len()) }

// Events is the visible window, oldest first.
//
// The result aliases the ring's storage and must be treated as read-only:
// appending to it would write into the array a later Add is going to use.
// Callers render it and nothing else.
func (r Ring) Events() []Event {
	if len(r.buf) <= r.cap {
		return r.buf
	}
	return r.buf[len(r.buf)-r.cap:]
}

// Tail is the newest n events, oldest first — the dashboard's twenty lines
// without carrying the other sixteen thousand through the render.
func (r Ring) Tail(n int) []Event {
	if n < 1 {
		return nil
	}
	all := r.Events()
	if len(all) <= n {
		return all
	}
	return all[len(all)-n:]
}
