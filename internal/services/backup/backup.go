// Package backup archives a server's data directory.
//
// A tar stream compressed with zstd, written next to the data it came from.
// DESIGN §11 names the format and the layout; zstd rather than gzip because a
// world directory is thousands of small similar files, which is the case zstd
// is dramatically better at, and a backup slow enough to skip is a backup
// nobody has.
package backup

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Extension is what an archive is called. Written out rather than assembled so
// a search for it finds every place that cares.
const Extension = ".tar.zst"

// Archive is one backup on disk.
type Archive struct {
	Path  string
	Name  string
	Taken time.Time
	Bytes int64
}

// Store is a server's backup directory.
type Store struct{ Dir string }

// Name is what an archive taken now is called.
//
// A sortable timestamp, so the directory listing is chronological without
// anything having to read the files — which is also why it is not the local
// format somebody would prefer to read.
func Name(at time.Time) string {
	return at.UTC().Format("2006-01-02T1504") + Extension
}

// Create archives src into the store and returns the archive.
//
// It writes to a temporary name and renames on success, so an interrupted
// backup does not leave a truncated archive that looks restorable. That is the
// failure this whole package exists to avoid: a backup you discover is
// unusable at the moment you need it.
func (s Store) Create(ctx context.Context, src string, at time.Time) (Archive, error) {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return Archive{}, fmt.Errorf("creating %s: %w", s.Dir, err)
	}
	if info, err := os.Stat(src); err != nil {
		return Archive{}, fmt.Errorf("reading %s: %w", src, err)
	} else if !info.IsDir() {
		return Archive{}, fmt.Errorf("%s is not a directory", src)
	}

	final := filepath.Join(s.Dir, Name(at))
	tmp, err := os.CreateTemp(s.Dir, ".partial-*"+Extension)
	if err != nil {
		return Archive{}, fmt.Errorf("creating a temporary archive: %w", err)
	}
	defer os.Remove(tmp.Name())

	if err := writeArchive(ctx, tmp, src); err != nil {
		tmp.Close()
		return Archive{}, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return Archive{}, fmt.Errorf("flushing the archive: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Archive{}, fmt.Errorf("closing the archive: %w", err)
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		return Archive{}, fmt.Errorf("naming the archive: %w", err)
	}

	info, err := os.Stat(final)
	if err != nil {
		return Archive{}, fmt.Errorf("reading back the archive: %w", err)
	}
	return Archive{Path: final, Name: filepath.Base(final), Taken: at, Bytes: info.Size()}, nil
}

func writeArchive(ctx context.Context, w io.Writer, src string) error {
	enc, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return fmt.Errorf("starting compression: %w", err)
	}
	defer enc.Close()

	tw := tar.NewWriter(enc)

	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		// Symlinks are stored as links rather than followed. Following them
		// can walk out of the data directory entirely, and a backup that
		// quietly includes the rest of the disk is one nobody can restore.
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			if link, err = os.Readlink(path); err != nil {
				return err
			}
		}

		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		if !info.Mode().IsRegular() {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return fmt.Errorf("archiving %s: %w", src, err)
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("finishing the archive: %w", err)
	}
	return enc.Close()
}

// List returns the archives in the store, newest first.
func (s Store) List() ([]Archive, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading %s: %w", s.Dir, err)
	}

	var out []Archive
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), Extension) || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Archive{
			Path:  filepath.Join(s.Dir, e.Name()),
			Name:  e.Name(),
			Taken: takenFrom(e.Name(), info.ModTime()),
			Bytes: info.Size(),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Taken.After(out[j].Taken) })
	return out, nil
}

// Restore unpacks an archive over dst.
//
// It refuses to write outside dst, which matters because an archive is a file
// somebody could have edited or fetched: a path of "../../etc" in a tar header
// is the oldest trick there is, and a restore running with the operator's
// privileges is exactly where it would pay off.
func (s Store) Restore(ctx context.Context, archive, dst string) error {
	f, err := os.Open(archive)
	if err != nil {
		return fmt.Errorf("opening %s: %w", archive, err)
	}
	defer f.Close()

	dec, err := zstd.NewReader(f)
	if err != nil {
		return fmt.Errorf("reading %s: %w", archive, err)
	}
	defer dec.Close()

	if err := os.MkdirAll(dst, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}
	root, err := filepath.Abs(dst)
	if err != nil {
		return err
	}

	tr := tar.NewReader(dec)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading %s: %w", archive, err)
		}

		target, err := safeJoin(root, header.Name)
		if err != nil {
			return fmt.Errorf("%s: %w", archive, err)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// A symlink is checked by where it points, not only by where it
			// is written. An absolute target escapes by definition, and a
			// relative one has to be resolved against the entry's own
			// directory before it can be judged — joining it onto the root
			// makes an absolute target look contained when it is not.
			if err := safeLink(root, header.Name, header.Linkname); err != nil {
				return fmt.Errorf("%s: %w", archive, err)
			}
			os.Remove(target)
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
}

// safeLink refuses a symlink that would point outside the restore directory.
//
// Restoring an archive is running somebody else's file list with the
// operator's privileges. An absolute target is refused outright; a relative
// one is resolved from the link's own directory, because "../../etc" from
// three levels down is contained and from the top is not.
func safeLink(root, name, linkname string) error {
	if filepath.IsAbs(linkname) || strings.HasPrefix(linkname, "/") {
		return fmt.Errorf("symlink %q points outside the restore directory (%q)", name, linkname)
	}
	// Windows drive letters are absolute too, and filepath.IsAbs on Linux
	// does not think so.
	if len(linkname) > 1 && linkname[1] == ':' {
		return fmt.Errorf("symlink %q points outside the restore directory (%q)", name, linkname)
	}

	resolved := filepath.Join(filepath.Dir(filepath.FromSlash(name)), filepath.FromSlash(linkname))
	if _, err := safeJoin(root, filepath.ToSlash(resolved)); err != nil {
		return fmt.Errorf("symlink %q escapes the restore directory", name)
	}
	return nil
}

// safeJoin resolves name inside root and refuses anything that escapes.
func safeJoin(root, name string) (string, error) {
	target := filepath.Join(root, filepath.FromSlash(name))
	if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("entry %q would write outside the restore directory", name)
	}
	return target, nil
}

// Prune keeps the newest n archives and deletes the rest, returning what it
// removed.
func (s Store) Prune(keep int) ([]string, error) {
	if keep < 1 {
		keep = 1
	}
	all, err := s.List()
	if err != nil || len(all) <= keep {
		return nil, err
	}

	var removed []string
	for _, a := range all[keep:] {
		if err := os.Remove(a.Path); err != nil {
			return removed, fmt.Errorf("removing %s: %w", a.Name, err)
		}
		removed = append(removed, a.Name)
	}
	return removed, nil
}

// takenFrom reads the timestamp out of an archive's name, falling back to the
// file's own time for anything renamed by hand.
func takenFrom(name string, fallback time.Time) time.Time {
	stamp := strings.TrimSuffix(name, Extension)
	if t, err := time.Parse("2006-01-02T1504", stamp); err == nil {
		return t.UTC()
	}
	return fallback.UTC()
}
