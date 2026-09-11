package mods

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/camden-brown/garrison/internal/model"
)

// manifestName records what Garrison installed, inside the directory it
// installed into.
//
// The manifest is what makes this safe to point at a directory a person also
// uses by hand: pruning removes what this file says Garrison put there and
// nothing else, so a DLL dropped in by the operator survives every sync. It
// also carries the installed version, which is the half an update badge
// cannot get from the source.
const manifestName = ".garrison-mods.json"

// stashName holds what a sync displaced, so the step that ran it can be
// undone.
//
// It is a sibling of the mod directory rather than a child, because anything
// inside the mod directory is a file the game will try to load.
const stashName = ".garrison-mods-prev"

// Releaser resolves a ref to the version that should be installed. Thunderstore
// implements it.
type Releaser interface {
	Release(ctx context.Context, ref model.ModRef) (Release, error)
}

// Install puts a server's mods on disk.
//
// It belongs to services rather than to the plugin because it downloads and
// writes files, and a plugin's methods are pure. What it does not know is
// which directory: the game says that through games.Installable and cmd
// resolves it against the instance's data, which is the same split the rest
// of the capability interfaces use.
type Install struct {
	// Dir is the absolute path of the directory mods are installed into.
	Dir string
	// Source resolves ids to downloads.
	Source Releaser
	// HTTP fetches the packages. Nil means a default with a generous
	// timeout, because a package is megabytes over somebody else's CDN.
	HTTP Client
	// Skip are ids the server image installs itself, from
	// games.Installable's Bundled.
	Skip []string
}

// entry is one installed mod as the manifest records it.
type entry struct {
	Version   string    `json:"version"`
	Bytes     int64     `json:"bytes"`
	Files     []string  `json:"files"`
	Installed time.Time `json:"installed"`
}

type manifest struct {
	Mods map[string]entry `json:"mods"`
}

// Installed reports what is on disk for one id, for the resolver to fill in.
func (i Install) Installed(id string) InstalledMod {
	m, err := i.read()
	if err != nil {
		return InstalledMod{}
	}
	e, ok := m.Mods[canonical(id)]
	if !ok {
		return InstalledMod{}
	}
	return InstalledMod{Version: e.Version, Bytes: e.Bytes}
}

// Sync makes the directory match the refs: installs what is missing or out of
// date, removes what Garrison installed and the config no longer lists.
//
// The returned undo puts back exactly what this call displaced. It is the
// compensation for the task step that calls Sync, which is required of any
// step that changes a volume — an apply that installs three mods and then
// fails to start the server must not leave the three mods behind.
func (i Install) Sync(ctx context.Context, refs []model.ModRef, log func(string)) (func(context.Context) error, error) {
	if log == nil {
		log = func(string) {}
	}
	if i.Dir == "" {
		return nil, errors.New("no directory to install mods into")
	}
	if i.Source == nil {
		return nil, errors.New("no source to resolve mods against")
	}
	if err := os.MkdirAll(i.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", i.Dir, err)
	}

	before, err := i.read()
	if err != nil {
		return nil, err
	}

	// A stash from a previous sync has already served its purpose: that task
	// either finished or was unwound. Clearing it here rather than on the
	// way out is what keeps one recoverable copy around after a successful
	// apply, which is the copy somebody wants when a mod update breaks their
	// world.
	stash := filepath.Join(filepath.Dir(i.Dir), stashName)
	if err := os.RemoveAll(stash); err != nil {
		return nil, fmt.Errorf("clearing %s: %w", stash, err)
	}

	u := &undoer{dir: i.Dir, stash: stash, before: before}
	after := manifest{Mods: map[string]entry{}}

	wanted := map[string]bool{}
	for _, ref := range refs {
		id := canonical(ref.ID)
		if id == "" {
			continue
		}
		if i.skipped(id) {
			log(id + ": installed by the server image, leaving it alone")
			continue
		}
		wanted[id] = true

		rel, err := i.Source.Release(ctx, ref)
		if err != nil {
			return u.undo, err
		}

		if have, ok := before.Mods[id]; ok && have.Version == rel.Version && i.present(id) {
			log(fmt.Sprintf("%s %s is up to date", id, have.Version))
			after.Mods[id] = have
			continue
		}

		if err := u.displace(id); err != nil {
			return u.undo, err
		}
		log(fmt.Sprintf("installing %s %s", id, rel.Version))
		written, bytes, err := i.fetch(ctx, rel, filepath.Join(i.Dir, id))
		if err != nil {
			return u.undo, err
		}
		u.added(id)
		after.Mods[id] = entry{Version: rel.Version, Bytes: bytes, Files: written, Installed: time.Now().UTC()}
	}

	// Anything Garrison installed and nobody asked for any more. Only what
	// the manifest claims, which is why a hand-dropped plugin is safe here.
	for id := range before.Mods {
		if wanted[id] {
			continue
		}
		log("removing " + id + ", which is no longer configured")
		if err := u.displace(id); err != nil {
			return u.undo, err
		}
	}

	if err := i.write(after); err != nil {
		return u.undo, err
	}
	return u.undo, nil
}

func (i Install) skipped(id string) bool {
	for _, s := range i.Skip {
		if strings.EqualFold(canonical(s), id) {
			return true
		}
	}
	return false
}

func (i Install) present(id string) bool {
	info, err := os.Stat(filepath.Join(i.Dir, id))
	return err == nil && info.IsDir()
}

// fetch downloads one release and unpacks it into dst.
func (i Install) fetch(ctx context.Context, rel Release, dst string) ([]string, int64, error) {
	client := i.HTTP
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rel.URL, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("downloading %s: %w", rel.ID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("downloading %s: Thunderstore answered %s", rel.ID, resp.Status)
	}

	// Into a temporary file rather than memory: these are tens of megabytes
	// for the larger packages, and archive/zip needs to seek anyway.
	tmp, err := os.CreateTemp("", "garrison-mod-*.zip")
	if err != nil {
		return nil, 0, err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	size, err := io.Copy(tmp, resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("downloading %s: %w", rel.ID, err)
	}

	zr, err := zip.NewReader(tmp, size)
	if err != nil {
		return nil, 0, fmt.Errorf("%s did not download a readable zip: %w", rel.ID, err)
	}
	return unpack(zr, dst)
}

// unpack writes the parts of a package that belong in a plugins directory.
//
// Thunderstore packages are a shallow convention rather than a format: the
// content is usually under plugins/, sometimes under BepInEx/plugins/, and
// occasionally a bare DLL at the root. All three land in the same place here,
// which is what makes an id enough for a person to type.
func unpack(zr *zip.Reader, dst string) ([]string, int64, error) {
	var (
		written []string
		total   int64
	)
	for _, f := range zr.File {
		if escapes(f.Name) {
			// Refused rather than skipped: a package that names a path
			// outside itself is not one to half-install and call done.
			return written, total, fmt.Errorf("%s contains a path that escapes the mod directory: %q",
				filepath.Base(dst), f.Name)
		}
		rel, ok := destination(f.Name)
		if !ok {
			continue
		}
		out := filepath.Join(dst, filepath.FromSlash(rel))

		// Zip entries are attacker-controlled names. Anything that escapes
		// the destination is refused rather than sanitised, because a
		// package that contains one is not a package to half-install.
		if !within(dst, out) {
			return written, total, fmt.Errorf("%s contains a path that escapes the mod directory: %q", filepath.Base(dst), f.Name)
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return written, total, err
		}

		src, err := f.Open()
		if err != nil {
			return written, total, err
		}
		dstFile, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			src.Close()
			return written, total, err
		}
		n, err := io.Copy(dstFile, src)
		src.Close()
		dstFile.Close()
		if err != nil {
			return written, total, err
		}
		written = append(written, rel)
		total += n
	}
	if len(written) == 0 {
		return nil, 0, errors.New("the package held no plugin files")
	}
	sort.Strings(written)
	return written, total, nil
}

// packaging is what Thunderstore requires in every package and what no game
// ever reads.
var packaging = map[string]bool{
	"manifest.json": true,
	"icon.png":      true,
	"readme.md":     true,
	"changelog.md":  true,
	"license":       true,
	"license.md":    true,
	"license.txt":   true,
}

// escapes reports a zip entry that names somewhere outside the archive.
func escapes(name string) bool {
	clean := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	return clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean)
}

// destination maps a zip entry to its path inside the mod's directory, or
// reports that it is not content.
func destination(name string) (string, bool) {
	clean := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	if clean == "." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	lower := strings.ToLower(clean)

	switch {
	case strings.HasPrefix(lower, "bepinex/plugins/"):
		return clean[len("BepInEx/plugins/"):], true
	case strings.HasPrefix(lower, "plugins/"):
		return clean[len("plugins/"):], true
	case strings.HasPrefix(lower, "bepinex/"):
		// patchers, core, config and the rest. A server-side install that
		// wrote these would be overwriting the loader the image manages and
		// the configuration the operator edits.
		return "", false
	case !strings.Contains(clean, "/") && packaging[lower]:
		return "", false
	default:
		return clean, true
	}
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// canonical is the id as the manifest and the directory name use it:
// Thunderstore's own full name, so "Owner/Package" and "Owner-Package" are
// one mod and not two.
func canonical(id string) string {
	id = strings.TrimSpace(id)
	if i := strings.IndexByte(id, '/'); i > 0 {
		return id[:i] + "-" + id[i+1:]
	}
	return id
}

func (i Install) read() (manifest, error) {
	out := manifest{Mods: map[string]entry{}}
	data, err := os.ReadFile(filepath.Join(i.Dir, manifestName))
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		// A corrupt manifest is not a reason to refuse to install; it is a
		// reason to stop claiming ownership of what it named. Starting from
		// empty means nothing is pruned, which errs towards leaving files
		// alone.
		return manifest{Mods: map[string]entry{}}, nil
	}
	if out.Mods == nil {
		out.Mods = map[string]entry{}
	}
	return out, nil
}

func (i Install) write(m manifest) error {
	data, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(i.Dir, manifestName), append(data, '\n'), 0o644)
}

// undoer records what a sync displaced so it can be put back.
//
// Moves, never copies: the stash is a sibling directory on the same
// filesystem, so displacing a 200 MB mod costs a rename and undoing it costs
// another. A compensation that is expensive is a compensation that gets
// skipped.
type undoer struct {
	dir    string
	stash  string
	before manifest
	moved  []string
	fresh  []string
}

func (u *undoer) displace(id string) error {
	from := filepath.Join(u.dir, id)
	if _, err := os.Stat(from); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err := os.MkdirAll(u.stash, 0o755); err != nil {
		return err
	}
	if err := os.Rename(from, filepath.Join(u.stash, id)); err != nil {
		return fmt.Errorf("setting aside %s: %w", id, err)
	}
	u.moved = append(u.moved, id)
	return nil
}

func (u *undoer) added(id string) { u.fresh = append(u.fresh, id) }

func (u *undoer) undo(context.Context) error {
	var first error
	keep := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}

	for _, id := range u.fresh {
		keep(os.RemoveAll(filepath.Join(u.dir, id)))
	}
	for _, id := range u.moved {
		from := filepath.Join(u.stash, id)
		if _, err := os.Stat(from); err != nil {
			continue
		}
		keep(os.Rename(from, filepath.Join(u.dir, id)))
	}
	// The manifest goes back last, so a failure above leaves it describing
	// more than is there rather than less: an over-claiming manifest prunes
	// a directory that is already gone, and an under-claiming one orphans
	// files nothing will ever remove.
	keep(Install{Dir: u.dir}.write(u.before))
	return first
}
