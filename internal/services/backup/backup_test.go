package backup_test

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/camden-brown/garrison/internal/services/backup"
)

var at = time.Date(2026, 9, 9, 2, 20, 0, 0, time.UTC)

// world builds something shaped like a save directory: many small files in
// nested directories, which is what these archives actually hold.
func world(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	files := map[string]string{
		"Huldra.fwl":           "world metadata",
		"Huldra.db":            strings.Repeat("chunk data ", 500),
		"chunks/0_0.dat":       "zone 0 0",
		"chunks/0_1.dat":       "zone 0 1",
		"chunks/deep/12_7.dat": "zone 12 7",
		"adminlist.txt":        "76561190000000001",
	}
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestBackupAndRestoreRoundTrip(t *testing.T) {
	src := world(t)
	store := backup.Store{Dir: filepath.Join(t.TempDir(), "backups")}

	archive, err := store.Create(context.Background(), src, at)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if archive.Bytes == 0 {
		t.Error("the archive is empty")
	}
	if archive.Name != "2026-09-09T0220.tar.zst" {
		t.Errorf("Name = %q, want a sortable timestamp", archive.Name)
	}

	// Restore somewhere else and compare, which is the only check that
	// matters: a backup is only real if it comes back.
	dst := filepath.Join(t.TempDir(), "restored")
	if err := store.Restore(context.Background(), archive.Path, dst); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}

	compare(t, src, dst)
}

// compare walks both trees and fails on any difference.
func compare(t *testing.T, want, got string) {
	t.Helper()

	seen := map[string]bool{}
	err := filepath.Walk(want, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(want, path)
		seen[rel] = true

		original, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		restored, err := os.ReadFile(filepath.Join(got, rel))
		if err != nil {
			t.Errorf("%s is missing from the restore: %v", rel, err)
			return nil
		}
		if !bytes.Equal(original, restored) {
			t.Errorf("%s came back different", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) == 0 {
		t.Fatal("the fixture had no files, so this proved nothing")
	}
}

// An interrupted backup must not leave a truncated archive that looks
// restorable — that is the failure the whole package exists to avoid.
func TestAnInterruptedBackupLeavesNothingRestorable(t *testing.T) {
	src := world(t)
	dir := filepath.Join(t.TempDir(), "backups")
	store := backup.Store{Dir: dir}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := store.Create(ctx, src, at); err == nil {
		t.Fatal("Create() succeeded with a cancelled context")
	}

	got, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("a cancelled backup left %d archives behind", len(got))
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		t.Errorf("scratch file left behind: %s", e.Name())
	}
}

// A tar header of "../../etc/passwd" is the oldest trick there is, and a
// restore runs with the operator's privileges.
func TestRestoreRefusesToEscapeItsDirectory(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "evil.tar.zst")

	var buf bytes.Buffer
	enc, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(enc)
	body := []byte("owned")
	if err := tw.WriteHeader(&tar.Header{
		Name: "../../escaped.txt", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	tw.Write(body)
	tw.Close()
	enc.Close()
	if err := os.WriteFile(archive, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "restore-here")
	err = backup.Store{Dir: dir}.Restore(context.Background(), archive, dst)
	if err == nil {
		t.Fatal("Restore() accepted a path that escapes the destination")
	}
	if !strings.Contains(err.Error(), "outside") {
		t.Errorf("error = %v, want it to say the entry escapes", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "escaped.txt")); err == nil {
		t.Fatal("the archive wrote outside its destination")
	}
}

// Symlinks are stored as links rather than followed. Following one can walk
// out of the data directory, and a backup that quietly includes the rest of
// the disk is one nobody can restore.
func TestSymlinksAreStoredNotFollowed(t *testing.T) {
	src := world(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("not part of the world"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(src, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	store := backup.Store{Dir: filepath.Join(t.TempDir(), "backups")}
	archive, err := store.Create(context.Background(), src, at)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// The link is stored as a link, not as its target's contents.
	if archive.Bytes > 4096 {
		t.Errorf("the archive is %d bytes, which suggests the link was followed", archive.Bytes)
	}

	// And restoring it is refused, because the target is outside.
	dst := filepath.Join(t.TempDir(), "restored")
	err = store.Restore(context.Background(), archive.Path, dst)
	if err == nil {
		t.Fatal("Restore() recreated a symlink pointing outside the destination")
	}
	if !strings.Contains(err.Error(), "outside") {
		t.Errorf("error = %v, want it to say the link escapes", err)
	}
}

// A relative link inside the tree is ordinary and must survive.
func TestRelativeSymlinksInsideTheTreeAreKept(t *testing.T) {
	src := world(t)
	if err := os.Symlink("Huldra.fwl", filepath.Join(src, "current.fwl")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	store := backup.Store{Dir: filepath.Join(t.TempDir(), "backups")}
	archive, err := store.Create(context.Background(), src, at)
	if err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "restored")
	if err := store.Restore(context.Background(), archive.Path, dst); err != nil {
		t.Fatalf("Restore() refused an ordinary relative link: %v", err)
	}
	if target, err := os.Readlink(filepath.Join(dst, "current.fwl")); err != nil || target != "Huldra.fwl" {
		t.Errorf("link came back as %q, %v", target, err)
	}
}

func TestListIsNewestFirst(t *testing.T) {
	src := world(t)
	store := backup.Store{Dir: filepath.Join(t.TempDir(), "backups")}

	for i := 0; i < 3; i++ {
		if _, err := store.Create(context.Background(), src, at.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d archives, want 3", len(got))
	}
	for i := 1; i < len(got); i++ {
		if !got[i-1].Taken.After(got[i].Taken) {
			t.Errorf("archives are not newest first: %v then %v", got[i-1].Taken, got[i].Taken)
		}
	}
}

func TestPruneKeepsTheNewest(t *testing.T) {
	src := world(t)
	store := backup.Store{Dir: filepath.Join(t.TempDir(), "backups")}

	for i := 0; i < 5; i++ {
		if _, err := store.Create(context.Background(), src, at.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := store.Prune(2)
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if len(removed) != 3 {
		t.Errorf("removed %d, want 3", len(removed))
	}

	left, _ := store.List()
	if len(left) != 2 {
		t.Fatalf("%d archives left, want 2", len(left))
	}
	// The newest must be the survivors, not whichever the filesystem
	// happened to list first.
	if !left[0].Taken.Equal(at.Add(4 * time.Hour)) {
		t.Errorf("kept %v, want the newest", left[0].Taken)
	}
}

func TestListOnAMissingDirectoryIsEmpty(t *testing.T) {
	got, err := backup.Store{Dir: filepath.Join(t.TempDir(), "never-made")}.List()
	if err != nil || len(got) != 0 {
		t.Errorf("List() = %v, %v, want empty and no error", got, err)
	}
}

func TestBackingUpSomethingThatIsNotThere(t *testing.T) {
	store := backup.Store{Dir: t.TempDir()}
	if _, err := store.Create(context.Background(), filepath.Join(t.TempDir(), "gone"), at); err == nil {
		t.Fatal("Create() succeeded for a directory that does not exist")
	}
}
