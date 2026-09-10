package backup

import (
	"context"
	"time"
)

// DefaultInterval is how often the archive directories are re-listed.
//
// Slow on purpose. Backups appear when a task makes one, which is minutes
// apart at best, and the poll exists to notice the ones Garrison did not make:
// a file copied away by hand, a directory restored from elsewhere. Listing a
// directory per server every thirty seconds would be work spent to watch
// nothing happen.
const DefaultInterval = 30 * time.Second

// Dirs names the backup directory for each server Garrison knows about.
//
// The service does not compute these itself, because working out where a
// server's archives live means reading its configured data directory, which
// means reading the store — and a service never imports core (ADR 0007). cmd
// knows both halves and supplies this.
type Dirs interface {
	BackupDirs() map[string]string
}

// Observer receives what the poller found.
type Observer interface {
	BackupsListed(ctx context.Context, at time.Time, server string, archives []Archive)
}

// Poller lists every server's archives on an interval.
//
// Failure to read one server's directory is not reported as an error and does
// not stop the others: a data directory that does not exist yet is the normal
// state of a server that has never been backed up, and an alert for it would
// fire on every fresh install. The list simply comes back empty, which is what
// the view will say.
type Poller struct {
	Interval time.Duration
	Dirs     Dirs
}

// Run polls until the context is cancelled, listing once immediately so the
// screen is populated before the first tick rather than thirty seconds after
// it.
func (p *Poller) Run(ctx context.Context, obs Observer) {
	if p.Dirs == nil || obs == nil {
		return
	}
	interval := p.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}

	p.once(ctx, obs)

	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			p.once(ctx, obs)
		}
	}
}

func (p *Poller) once(ctx context.Context, obs Observer) {
	for server, dir := range p.Dirs.BackupDirs() {
		if dir == "" {
			continue
		}
		// An unreadable directory reports as none rather than as an error.
		// A server that has never been backed up has no directory at all.
		archives, _ := Store{Dir: dir}.List()
		obs.BackupsListed(ctx, time.Now(), server, archives)
	}
}
