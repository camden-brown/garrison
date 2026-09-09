package metrics

import (
	"testing"
	"time"
)

var base = time.Date(2026, 9, 9, 21, 0, 0, 0, time.UTC)

func at(sec int) time.Time { return base.Add(time.Duration(sec) * time.Second) }

func means(pts []Point) []float64 {
	out := make([]float64, len(pts))
	for i, p := range pts {
		out[i] = p.Mean
	}
	return out
}

func equal(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// addAll returns the history plus every cold point that completed along the
// way, which is what a caller persisting the cold tier would collect.
func addAll(t Tiered, from, count int, v func(i int) float64) (Tiered, []Point) {
	var cold []Point
	for i := from; i < from+count; i++ {
		next, c, ok := t.Add(at(i), v(i))
		t = next
		if ok {
			cold = append(cold, c)
		}
	}
	return t, cold
}

func constant(x float64) func(int) float64 { return func(int) float64 { return x } }

func TestZeroValueIsUsable(t *testing.T) {
	var h Tiered
	h, _, _ = h.Add(at(0), 42)

	if got, ok := h.Last(); !ok || got.Mean != 42 {
		t.Errorf("Last() = %v (ok=%v), want 42", got.Mean, ok)
	}
}

func TestHotKeepsEverySample(t *testing.T) {
	h, _ := addAll(Tiered{}, 0, 5, func(i int) float64 { return float64(i) })

	if got := means(h.Hot); !equal(got, []float64{0, 1, 2, 3, 4}) {
		t.Errorf("Hot = %v, want every sample oldest first", got)
	}
}

// This is the property everything else rests on: a history already handed to a
// renderer must never change underneath it.
func TestAddDoesNotTouchTheOldHistory(t *testing.T) {
	before, _ := addAll(Tiered{}, 0, 3, func(i int) float64 { return float64(i) })
	snapshot := before.Hot

	after, _, _ := before.Add(at(3), 99)

	if got := means(snapshot); !equal(got, []float64{0, 1, 2}) {
		t.Errorf("the published slice changed: %v", got)
	}
	if got := means(before.Hot); !equal(got, []float64{0, 1, 2}) {
		t.Errorf("the old value changed: %v", got)
	}
	if got := means(after.Hot); !equal(got, []float64{0, 1, 2, 99}) {
		t.Errorf("the new value = %v, want the sample appended", got)
	}
}

// Once full, appending must not alias the slice a snapshot is still holding.
func TestAddDoesNotAliasOnceFull(t *testing.T) {
	full, _ := addAll(Tiered{}, 0, HotCap, func(i int) float64 { return float64(i) })
	held := full.Hot

	_, _, _ = full.Add(at(HotCap), -1)

	if held[len(held)-1].Mean != float64(HotCap-1) {
		t.Errorf("the held slice was written through: last = %v", held[len(held)-1].Mean)
	}
	if held[0].Mean != 0 {
		t.Errorf("the held slice was shifted: first = %v", held[0].Mean)
	}
}

func TestWarmAggregatesTenHotSamples(t *testing.T) {
	h, _ := addAll(Tiered{}, 0, HotPerWarm-1, constant(1))
	if len(h.Warm) != 0 {
		t.Errorf("Warm has %d points before the window closed, want 0", len(h.Warm))
	}

	h, _, _ = h.Add(at(HotPerWarm-1), 1)
	if len(h.Warm) != 1 {
		t.Fatalf("Warm has %d points after %d samples, want 1", len(h.Warm), HotPerWarm)
	}
}

// The reason min and max are carried at all.
func TestWarmPointKeepsTheExtremes(t *testing.T) {
	h, _ := addAll(Tiered{}, 0, HotPerWarm, func(i int) float64 {
		if i == 4 {
			return 100 // one second pinned
		}
		return 10
	})

	if len(h.Warm) != 1 {
		t.Fatalf("got %d warm points, want 1", len(h.Warm))
	}
	p := h.Warm[0]
	if p.Max != 100 {
		t.Errorf("Max = %v, want the spike at 100", p.Max)
	}
	if p.Min != 10 {
		t.Errorf("Min = %v, want 10", p.Min)
	}
	if want := (9*10.0 + 100) / 10; p.Mean != want {
		t.Errorf("Mean = %v, want %v", p.Mean, want)
	}
	if !p.At.Equal(at(HotPerWarm - 1)) {
		t.Errorf("At = %v, want the last observation — a point must not describe a moment that has not happened", p.At)
	}
}

func TestColdCompletesEverySixWarmPoints(t *testing.T) {
	perCold := HotPerWarm * WarmPerCold

	h, cold := addAll(Tiered{}, 0, perCold-1, constant(5))
	if len(cold) != 0 {
		t.Fatalf("got %d cold points early", len(cold))
	}

	_, c, ok := h.Add(at(perCold-1), 5)
	if !ok {
		t.Fatal("no cold point after a full minute of samples")
	}
	if c.Mean != 5 {
		t.Errorf("cold mean = %v, want 5", c.Mean)
	}
}

// An extreme must survive being averaged twice.
func TestExtremesSurviveTwoDownsamples(t *testing.T) {
	_, cold := addAll(Tiered{}, 0, HotPerWarm*WarmPerCold, func(i int) float64 {
		if i == 33 {
			return 500
		}
		return 1
	})

	if len(cold) != 1 {
		t.Fatalf("got %d cold points, want 1", len(cold))
	}
	if cold[0].Max != 500 {
		t.Errorf("cold Max = %v, want the spike to have survived", cold[0].Max)
	}
	if cold[0].Min != 1 {
		t.Errorf("cold Min = %v, want 1", cold[0].Min)
	}
}

// The leak with a long fuse this package exists to avoid.
func TestStaysBoundedOverHours(t *testing.T) {
	h, _ := addAll(Tiered{}, 0, 6*60*60, func(i int) float64 { return float64(i % 17) })

	if len(h.Hot) != HotCap {
		t.Errorf("hot holds %d points, want the cap %d", len(h.Hot), HotCap)
	}
	if len(h.Warm) != WarmCap {
		t.Errorf("warm holds %d points, want the cap %d", len(h.Warm), WarmCap)
	}
}

func TestHotDropsTheOldestOnceFull(t *testing.T) {
	h, _ := addAll(Tiered{}, 0, HotCap+3, func(i int) float64 { return float64(i) })

	if got := h.Hot[0].Mean; got != 3 {
		t.Errorf("oldest hot point = %v, want 3", got)
	}
	if got := h.Hot[len(h.Hot)-1].Mean; got != float64(HotCap+2) {
		t.Errorf("newest hot point = %v, want %v", got, HotCap+2)
	}
}

func TestTails(t *testing.T) {
	h, _ := addAll(Tiered{}, 0, 5, func(i int) float64 { return float64(i) })

	if got := means(h.HotTail(2)); !equal(got, []float64{3, 4}) {
		t.Errorf("HotTail(2) = %v, want [3 4]", got)
	}
	if got := means(h.HotTail(99)); !equal(got, []float64{0, 1, 2, 3, 4}) {
		t.Errorf("HotTail(99) = %v, want everything", got)
	}
	if got := h.HotTail(0); got != nil {
		t.Errorf("HotTail(0) = %v, want nil", got)
	}
	if got := (Tiered{}).HotTail(5); got != nil {
		t.Errorf("HotTail on an empty history = %v, want nil", got)
	}
}

func TestLastIsARawSampleNotAnAggregate(t *testing.T) {
	h, _ := addAll(Tiered{}, 0, 25, func(i int) float64 { return float64(i) })

	last, ok := h.Last()
	if !ok || last.Mean != 24 {
		t.Errorf("Last() = %v (ok=%v), want the newest raw sample 24", last.Mean, ok)
	}
}

func TestResolutionConstantsAgreeWithTheTable(t *testing.T) {
	if HotPerWarm != 10 {
		t.Errorf("HotPerWarm = %d, want 10", HotPerWarm)
	}
	if WarmPerCold != 6 {
		t.Errorf("WarmPerCold = %d, want 6", WarmPerCold)
	}
	if got := time.Duration(HotCap) * HotResolution; got != 5*time.Minute {
		t.Errorf("the hot tier spans %v, want 5 minutes", got)
	}
	if got := time.Duration(WarmCap) * WarmResolution; got != time.Hour {
		t.Errorf("the warm tier spans %v, want 1 hour", got)
	}
}
