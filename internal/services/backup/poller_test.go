package backup_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/camden-brown/garrison/internal/services/backup"
)

type stubDirs struct{ dirs map[string]string }

func (s stubDirs) BackupDirs() map[string]string { return s.dirs }

type recorder struct {
	mu   sync.Mutex
	seen map[string][]backup.Archive
	done chan struct{}
	once sync.Once
}

func newRecorder() *recorder {
	return &recorder{seen: map[string][]backup.Archive{}, done: make(chan struct{})}
}

func (r *recorder) BackupsListed(_ context.Context, _ time.Time, server string, archives []backup.Archive) {
	r.mu.Lock()
	r.seen[server] = archives
	r.mu.Unlock()
	r.once.Do(func() { close(r.done) })
}

func (r *recorder) get(server string) []backup.Archive {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen[server]
}

// touch writes an archive-shaped file so List has something to find.
func touch(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("not really an archive"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPollerListsEachServersArchives(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "2026-09-09T0300"+backup.Extension)
	touch(t, dir, "2026-09-08T0300"+backup.Extension)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rec := newRecorder()
	p := &backup.Poller{Interval: time.Hour, Dirs: stubDirs{dirs: map[string]string{"a": dir}}}
	go p.Run(ctx, rec)

	select {
	case <-rec.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the poller never listed anything")
	}

	if got := rec.get("a"); len(got) != 2 {
		t.Errorf("listed %d archives, want 2", len(got))
	}
}

// A server that has never been backed up has no directory, and that is the
// normal state of a fresh install rather than something to alert about.
func TestPollerReportsAMissingDirectoryAsNone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rec := newRecorder()
	missing := filepath.Join(t.TempDir(), "never-created")
	p := &backup.Poller{Interval: time.Hour, Dirs: stubDirs{dirs: map[string]string{"a": missing}}}
	go p.Run(ctx, rec)

	select {
	case <-rec.done:
	case <-time.After(2 * time.Second):
		t.Fatal("the poller never reported the missing directory")
	}

	if got := rec.get("a"); len(got) != 0 {
		t.Errorf("listed %d archives for a directory that does not exist, want 0", len(got))
	}
}

// The first listing happens immediately. Waiting a full interval would leave
// the Backups screen empty for thirty seconds after it is opened.
func TestPollerListsBeforeTheFirstTick(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "2026-09-09T0300"+backup.Extension)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rec := newRecorder()
	// An interval long enough that a tick cannot be what produced the answer.
	p := &backup.Poller{Interval: time.Hour, Dirs: stubDirs{dirs: map[string]string{"a": dir}}}
	go p.Run(ctx, rec)

	select {
	case <-rec.done:
	case <-time.After(2 * time.Second):
		t.Fatal("nothing was listed before the first tick")
	}
}

func TestPollerStopsWithTheContext(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "2026-09-09T0300"+backup.Extension)

	ctx, cancel := context.WithCancel(context.Background())
	rec := newRecorder()

	stopped := make(chan struct{})
	go func() {
		(&backup.Poller{Interval: time.Millisecond, Dirs: stubDirs{dirs: map[string]string{"a": dir}}}).Run(ctx, rec)
		close(stopped)
	}()

	<-rec.done
	cancel()

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("the poller outlived its context")
	}
}

// Nothing to poll and nowhere to send it are both no-ops rather than panics:
// cmd builds this before the first config is read.
func TestPollerWithNothingWiredReturns(t *testing.T) {
	done := make(chan struct{})
	go func() {
		(&backup.Poller{}).Run(context.Background(), nil)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a poller with nothing wired did not return")
	}
}
