package model

import "time"

// The three tiers from docs/DESIGN.md §8.
//
// Note the resolutions rather than the spans: DESIGN's prose says "a full hot
// ring flushes its mean, min and max into one warm point", which would make
// warm 5-minute resolution and contradict its own table. The table is the
// precise statement, so a warm point aggregates HotPerWarm hot samples and a
// cold point aggregates WarmPerCold warm ones.
const (
	HotResolution = time.Second
	HotCap        = 300 // 5 minutes

	WarmResolution = 10 * time.Second
	WarmCap        = 360 // 1 hour
	HotPerWarm     = int(WarmResolution / HotResolution)

	ColdResolution = time.Minute
	WarmPerCold    = int(ColdResolution / WarmResolution)
)

// Point is one sample, or one aggregate of samples.
//
// Min and Max travel with Mean because a downsampled average hides exactly
// what you went looking for. A server that pinned one core for four seconds
// averages to nothing remarkable over ten; the max is the reason you opened
// the dashboard.
type Point struct {
	At   time.Time
	Mean float64
	Min  float64
	Max  float64
}

// History is one measured quantity at two in-memory resolutions: CPU, memory,
// players, or whatever fourth metric a game's Parse emits.
//
// It is a value, not a buffer with a pointer to it. That is not style: a
// History lives inside core.Snapshot, snapshots are handed to a renderer on
// another goroutine, and a history the producer could still append to is a
// sparkline drawn from a row that changed halfway through. Add returns a new
// History and leaves the old one alone.
//
// It lives in model rather than in the service that fills it for the same
// reason State does: a service produces it and a view renders it, so it is
// shared vocabulary and neither end should have to import the other.
//
// The zero value is an empty history and is ready to use. Add returns the new
// history rather than modifying this one, so a History already published inside
// a snapshot can never change.
//
// The cold tier is not stored here. It is 30 days of history and belongs in
// SQLite, which arrives with the task engine's persistence at M2. Add returns
// completed cold points so the caller can hand them onward; today nothing
// does, and they are discarded.
type History struct {
	Hot  []Point // 1s resolution, newest last, at most HotCap
	Warm []Point // 10s resolution, newest last, at most WarmCap

	pendingWarm accumulator // hot samples not yet rolled up
	pendingCold accumulator // warm points not yet rolled up
}

// Add records one sample and returns the updated history.
//
// When a sample completes a cold-resolution window the aggregate comes back
// with ok true. Downsampling happens here rather than on a timer: the
// arithmetic is a few additions, so there is no second goroutine to shut down
// and no window in which the coarser tiers are stale.
func (t History) Add(at time.Time, v float64) (next History, cold Point, ok bool) {
	t.Hot = appendBounded(t.Hot, Point{At: at, Mean: v, Min: v, Max: v}, HotCap)

	t.pendingWarm.add(at, v)
	if t.pendingWarm.n < HotPerWarm {
		return t, Point{}, false
	}

	warm := t.pendingWarm.point()
	t.pendingWarm = accumulator{}
	t.Warm = appendBounded(t.Warm, warm, WarmCap)

	// The warm point feeds the cold window carrying its own min and max, so
	// an extreme survives two rounds of averaging instead of being smoothed
	// away before the cold tier ever sees it.
	t.pendingCold.merge(warm)
	if t.pendingCold.n < WarmPerCold {
		return t, Point{}, false
	}

	cold = t.pendingCold.point()
	t.pendingCold = accumulator{}
	return t, cold, true
}

// Last is the most recent raw sample — the live number beside the sparkline.
func (t History) Last() (Point, bool) {
	if len(t.Hot) == 0 {
		return Point{}, false
	}
	return t.Hot[len(t.Hot)-1], true
}

// HotTail returns the newest n hot points, which is what a sparkline of a
// known width asks for. Asking for more than it holds returns what it has.
func (t History) HotTail(n int) []Point { return tail(t.Hot, n) }

// WarmTail is the same for the 10-second tier.
func (t History) WarmTail(n int) []Point { return tail(t.Warm, n) }

func tail(pts []Point, n int) []Point {
	if n <= 0 || len(pts) == 0 {
		return nil
	}
	if n > len(pts) {
		n = len(pts)
	}
	return pts[len(pts)-n:]
}

// appendBounded returns a new slice with p appended, dropping the oldest point
// if that would exceed max.
//
// It always allocates. Sharing a backing array with the slice already sitting
// in a published snapshot is the one bug this whole package is shaped to
// avoid, and at 300 points the copy is a few microseconds.
func appendBounded(pts []Point, p Point, max int) []Point {
	if max < 1 {
		max = 1
	}
	if len(pts) < max {
		out := make([]Point, len(pts)+1)
		copy(out, pts)
		out[len(pts)] = p
		return out
	}
	out := make([]Point, max)
	copy(out, pts[len(pts)-max+1:])
	out[max-1] = p
	return out
}

// accumulator gathers observations into one aggregate point.
type accumulator struct {
	n    int
	sum  float64
	min  float64
	max  float64
	last time.Time
}

func (a *accumulator) add(at time.Time, v float64) {
	a.merge(Point{At: at, Mean: v, Min: v, Max: v})
}

func (a *accumulator) merge(p Point) {
	if a.n == 0 {
		a.min, a.max = p.Min, p.Max
	}
	if p.Min < a.min {
		a.min = p.Min
	}
	if p.Max > a.max {
		a.max = p.Max
	}
	a.sum += p.Mean
	a.last = p.At
	a.n++
}

// point closes the window, stamped with the last observation's time so a point
// never claims to describe a moment that has not happened yet.
func (a accumulator) point() Point {
	if a.n == 0 {
		return Point{}
	}
	return Point{At: a.last, Mean: a.sum / float64(a.n), Min: a.min, Max: a.max}
}
