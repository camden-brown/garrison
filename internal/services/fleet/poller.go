// Package fleet keeps the store's picture of the container fleet current, and
// performs the two operations the Fleet view offers.
//
// This is where the I/O lives. The store above it is a reducer with a mailbox
// and does no I/O of its own; the driver below it knows nothing about who is
// asking. Neither imports this package: a poller is handed an Observer and a
// Driver by cmd/garrison, which is the only place that knows both halves
// exist.
package fleet

import (
	"context"
	"time"

	"github.com/camden-brown/garrison/internal/host"
)

// DefaultInterval is how often the fleet is re-listed.
//
// Five seconds, not one. This process is meant to be left open for weeks, and
// "refresh everything every second" is how a background TUI ends up burning a
// core forever. State changes the operator causes are reported immediately by
// the operation that caused them; the poll is for everything else.
const DefaultInterval = 5 * time.Second

// Observer receives what a poll found. The store implements it.
//
// It is an interface declared here, by the consumer's producer, rather than a
// channel of a type the store would have to import. That is the seam: a
// service never imports internal/core, so the store cannot become something
// the I/O layer reaches into.
type Observer interface {
	FleetObserved(ctx context.Context, at time.Time, containers []host.Container)
	FleetUnobservable(ctx context.Context, at time.Time, err error)

	// HostDescribed carries the engine's own figures — version, cores,
	// total memory. They are what turn a container's usage into a
	// proportion, and none of them change while Garrison is running.
	HostDescribed(ctx context.Context, at time.Time, info host.Info)
}

// Poller re-lists the fleet on an interval.
type Poller struct {
	Driver   host.Driver
	Interval time.Duration
	Now      func() time.Time
}

// Run polls until ctx is cancelled, then returns. It blocks, so the caller
// owns the goroutine and can wait for it — there is no goroutine started in
// here that outlives the call.
func (p *Poller) Run(ctx context.Context, obs Observer) {
	interval := p.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}

	// One ticker for the life of the loop. time.After inside a loop allocates
	// a timer per iteration that stays live until it fires, which over a
	// fortnight of five-second polls is a lot of garbage for no reason.
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	p.describeHost(ctx, obs)
	p.poll(ctx, obs) // immediately, so the first frame is not empty for five seconds

	// The host's shape does not change, but the engine restarts, so it is
	// re-read occasionally rather than exactly once. Rarely enough that it
	// costs nothing; often enough that a Docker Desktop upgrade is noticed.
	described := time.NewTicker(hostInterval)
	defer described.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-described.C:
			p.describeHost(ctx, obs)
		case <-ticker.C:
			p.poll(ctx, obs)
		}
	}
}

// hostInterval is how often the engine's own figures are re-read.
const hostInterval = 5 * time.Minute

func (p *Poller) describeHost(ctx context.Context, obs Observer) {
	info, err := p.Driver.Info(ctx)
	if err != nil || ctx.Err() != nil {
		// Not worth reporting: the fleet poll is about to fail too and will
		// say so once, rather than twice for the same outage.
		return
	}
	obs.HostDescribed(ctx, p.now(), info)
}

// poll asks the driver for the fleet and reports whatever came back.
//
// It leans on List's error rather than calling Ping first: a failed list is
// the same fact a failed ping would report, and a second round trip on every
// tick would buy nothing but load.
func (p *Poller) poll(ctx context.Context, obs Observer) {
	containers, err := p.Driver.List(ctx)
	if ctx.Err() != nil {
		// Cancelled mid-call during shutdown. The error is our own doing, and
		// reporting it would raise "docker unreachable" on the way out.
		return
	}
	if err != nil {
		obs.FleetUnobservable(ctx, p.now(), err)
		return
	}
	obs.FleetObserved(ctx, p.now(), containers)
}

// now is the clock, injectable so a test asserts on a timestamp rather than
// racing one.
func (p *Poller) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}
