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
// It sits at the root of the instance's data rather than inside any mod
// directory, because anything inside one of those is a file the game will try
// to load.
const stashName = ".garrison-mods-prev"

// Layout is where each kind of content in a package belongs, relative to the
// instance's data. It mirrors games.ModLayout, which is where the answer
// comes from; this package cannot import games.
type Layout struct {
	Plugins  string
	Patchers string
	Config   string
}

// dirFor returns the destination for a kind of content, and whether this game
// has one at all.
func (l Layout) dirFor(k kind) (string, bool) {
	switch k {
	case kindPlugin:
		return l.Plugins, l.Plugins != ""
	case kindPatcher:
		return l.Patchers, l.Patchers != ""
	case kindConfig:
		return l.Config, l.Config != ""
	}
	return "", false
}

// kind is what a file inside a package is.
type kind uint8

const (
	kindSkip kind = iota
	kindPlugin
	kindPatcher
	kindConfig
)

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
	// Root is the absolute path of the instance's data directory. Every
	// destination in Layout is relative to it.
	Root string
	// Layout is where the parts of a package go.
	Layout Layout
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
	Version string `json:"version"`
	Bytes   int64  `json:"bytes"`
	// Dirs is every directory this mod occupies, relative to the instance's
	// data and slash-separated. A package can land in more than one — a
	// plugin and a patcher are read from different places — and pruning has
	// to know all of them or it leaves half a mod behind.
	Dirs      []string  `json:"dirs"`
	Installed time.Time `json:"installed"`
}

type manifest struct {
	Mods map[string]entry `json:"mods"`
}

// pluginsDir is where plugins go, and where the manifest lives beside them.
func (i Install) pluginsDir() string {
	return filepath.Join(i.Root, filepath.FromSlash(i.Layout.Plugins))
}

// dirsFor is every directory a package with this content would occupy,
// relative to Root and in slash form, which is how the manifest records them.
func (i Install) dirFor(k kind, id string) (string, bool) {
	dir, ok := i.Layout.dirFor(k)
	if !ok {
		return "", false
	}
	return dir + "/" + id, true
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
	if i.Root == "" || i.Layout.Plugins == "" {
		return nil, errors.New("no directory to install mods into")
	}
	if i.Source == nil {
		return nil, errors.New("no source to resolve mods against")
	}
	if err := os.MkdirAll(i.pluginsDir(), 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", i.pluginsDir(), err)
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
	stash := filepath.Join(i.Root, stashName)
	if err := os.RemoveAll(stash); err != nil {
		return nil, fmt.Errorf("clearing %s: %w", stash, err)
	}

	u := &undoer{root: i.Root, stash: stash, manifestDir: i.pluginsDir(), before: before}
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

		if have, ok := before.Mods[id]; ok && have.Version == rel.Version && i.present(have) {
			log(fmt.Sprintf("%s %s is up to date", id, have.Version))
			after.Mods[id] = have
			continue
		}

		if err := u.displace(dirsOf(before.Mods[id], i, id)); err != nil {
			return u.undo, err
		}
		log(fmt.Sprintf("installing %s %s", id, rel.Version))
		dirs, bytes, err := i.fetch(ctx, rel, id)
		if err != nil {
			return u.undo, err
		}
		u.added(dirs)
		after.Mods[id] = entry{Version: rel.Version, Bytes: bytes, Dirs: dirs, Installed: time.Now().UTC()}
	}

	// Anything Garrison installed and nobody asked for any more. Only what
	// the manifest claims, which is why a hand-dropped plugin is safe here.
	for id, was := range before.Mods {
		if wanted[id] {
			continue
		}
		log("removing " + id + ", which is no longer configured")
		if err := u.displace(dirsOf(was, i, id)); err != nil {
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

// present reports whether what the manifest claims is still on disk. A
// directory somebody deleted by hand is a mod to reinstall, not one to skip
// as current.
func (i Install) present(e entry) bool {
	if len(e.Dirs) == 0 {
		return false
	}
	for _, d := range e.Dirs {
		info, err := os.Stat(filepath.Join(i.Root, filepath.FromSlash(d)))
		if err != nil || !info.IsDir() {
			return false
		}
	}
	return true
}

// configOrDir resolves the destination for one kind of content. Config goes
// to the layout's config directory itself; everything else to a directory
// named after the mod, so pruning is exact.
func (i Install) configOrDir(k kind, id string) (string, bool) {
	if k == kindConfig {
		return i.Layout.dirFor(k)
	}
	return i.dirFor(k, id)
}

// dirsOf is where a mod's files are: what the manifest recorded, or — for an
// entry written before directories were recorded, and for a mod being
// installed for the first time — every directory this layout could have put
// them in.
func dirsOf(e entry, i Install, id string) []string {
	if len(e.Dirs) > 0 {
		return e.Dirs
	}
	var out []string
	for _, k := range []kind{kindPlugin, kindPatcher} {
		if dir, ok := i.dirFor(k, id); ok {
			out = append(out, dir)
		}
	}
	return out
}

// fetch downloads one release and unpacks it into the directories its
// contents belong in, returning those directories relative to Root.
func (i Install) fetch(ctx context.Context, rel Release, id string) ([]string, int64, error) {
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
	return i.unpack(zr, id)
}

// unpack writes the parts of a package into the directories they belong in.
//
// Thunderstore packages are a shallow convention rather than a format: the
// content is usually under plugins/, sometimes under BepInEx/plugins/, and
// occasionally a bare DLL at the root. Patchers are their own kind and their
// own directory — HookGenPatcher ships nothing else — and a package can carry
// both, which is why this returns a set of directories rather than one.
func (i Install) unpack(zr *zip.Reader, id string) ([]string, int64, error) {
	var (
		total int64
		used  = map[string]bool{}
		wrote bool
	)
	for _, f := range zr.File {
		if escapes(f.Name) {
			// Refused rather than skipped: a package that names a path
			// outside itself is not one to half-install and call done.
			return dirList(used), total, fmt.Errorf("%s contains a path that escapes the mod directory: %q", id, f.Name)
		}
		k, rel := classify(f.Name)
		if k == kindSkip {
			continue
		}
		// Config is the one kind that is not filed under the mod's own
		// directory: BepInEx names config files after the plugin's GUID and
		// reads them from one flat directory, which is also where the
		// operator's edits live.
		dir, ok := i.configOrDir(k, id)
		if !ok {
			// A package carrying something this game has nowhere to put. A
			// patcher installed among the plugins is a file nothing reads,
			// so this refuses rather than pretending.
			return dirList(used), total, fmt.Errorf("%s carries files this game has nowhere to install: %q", id, f.Name)
		}
		if k != kindConfig {
			used[dir] = true
		}

		root := filepath.Join(i.Root, filepath.FromSlash(dir))
		out := filepath.Join(root, filepath.FromSlash(rel))

		// Zip entries are attacker-controlled names. Anything that escapes
		// the destination is refused rather than sanitised, because a
		// package that contains one is not a package to half-install.
		if !within(root, out) {
			return dirList(used), total, fmt.Errorf("%s contains a path that escapes the mod directory: %q", id, f.Name)
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if k == kindConfig {
			// Never over an existing one. The file on disk is the
			// operator's, whatever wrote it first, and an update that
			// silently restored the author's defaults would undo an
			// afternoon of tuning without saying so.
			if _, err := os.Stat(out); err == nil {
				continue
			}
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return dirList(used), total, err
		}

		src, err := f.Open()
		if err != nil {
			return dirList(used), total, err
		}
		dstFile, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			src.Close()
			return dirList(used), total, err
		}
		n, err := io.Copy(dstFile, src)
		src.Close()
		dstFile.Close()
		if err != nil {
			return dirList(used), total, err
		}
		wrote = true
		total += n
	}
	if !wrote && len(used) == 0 {
		return nil, 0, errors.New("the package held no files this game can load")
	}
	return dirList(used), total, nil
}

// dirList is the set of directories a package occupied, in a stable order so
// the manifest does not churn between syncs that changed nothing.
func dirList(used map[string]bool) []string {
	out := make([]string, 0, len(used))
	for dir := range used {
		out = append(out, dir)
	}
	sort.Strings(out)
	return out
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

// classify says what a zip entry is and where inside its directory it goes.
func classify(name string) (kind, string) {
	clean := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	if clean == "." || strings.HasPrefix(clean, "../") {
		return kindSkip, ""
	}
	lower := strings.ToLower(clean)

	switch {
	case strings.HasPrefix(lower, "bepinex/plugins/"):
		return kindPlugin, clean[len("BepInEx/plugins/"):]
	case strings.HasPrefix(lower, "plugins/"):
		return kindPlugin, clean[len("plugins/"):]
	case strings.HasPrefix(lower, "bepinex/patchers/"):
		return kindPatcher, clean[len("BepInEx/patchers/"):]
	case strings.HasPrefix(lower, "patchers/"):
		return kindPatcher, clean[len("patchers/"):]
	case strings.HasPrefix(lower, "bepinex/config/"):
		return kindConfig, clean[len("BepInEx/config/"):]
	case strings.HasPrefix(lower, "config/"):
		return kindConfig, clean[len("config/"):]
	case strings.HasPrefix(lower, "bepinex/"):
		// The loader's own files. Writing these would overwrite what the
		// image manages, on every update.
		return kindSkip, ""
	case !strings.Contains(clean, "/") && packaging[lower]:
		return kindSkip, ""
	default:
		// A bare DLL at the root, or the translation files that sit beside
		// one. Plugins by convention, and the convention is what the mod
		// managers implement.
		return kindPlugin, clean
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
	data, err := os.ReadFile(filepath.Join(i.pluginsDir(), manifestName))
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

func (i Install) write(m manifest) error { return writeManifest(i.pluginsDir(), m) }

func writeManifest(dir string, m manifest) error {
	data, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, manifestName), append(data, '\n'), 0o644)
}

// undoer records what a sync displaced so it can be put back.
//
// Moves, never copies: the stash is a sibling directory on the same
// filesystem, so displacing a 200 MB mod costs a rename and undoing it costs
// another. A compensation that is expensive is a compensation that gets
// skipped.
type undoer struct {
	root        string
	stash       string
	manifestDir string
	before      manifest
	moved       []string
	fresh       []string
}

// stashKey flattens a relative directory into one stash entry, so a mod that
// occupies bepinex/plugins/X and bepinex/patchers/X sets both aside without
// one overwriting the other.
func stashKey(rel string) string { return strings.ReplaceAll(rel, "/", "%") }

func (u *undoer) displace(dirs []string) error {
	for _, rel := range dirs {
		from := filepath.Join(u.root, filepath.FromSlash(rel))
		if _, err := os.Stat(from); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err := os.MkdirAll(u.stash, 0o755); err != nil {
			return err
		}
		if err := os.Rename(from, filepath.Join(u.stash, stashKey(rel))); err != nil {
			return fmt.Errorf("setting aside %s: %w", rel, err)
		}
		u.moved = append(u.moved, rel)
	}
	return nil
}

func (u *undoer) added(dirs []string) { u.fresh = append(u.fresh, dirs...) }

func (u *undoer) undo(context.Context) error {
	var first error
	keep := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}

	for _, rel := range u.fresh {
		keep(os.RemoveAll(filepath.Join(u.root, filepath.FromSlash(rel))))
	}
	for _, rel := range u.moved {
		from := filepath.Join(u.stash, stashKey(rel))
		if _, err := os.Stat(from); err != nil {
			continue
		}
		to := filepath.Join(u.root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			keep(err)
			continue
		}
		keep(os.Rename(from, to))
	}
	// The manifest goes back last, so a failure above leaves it describing
	// more than is there rather than less: an over-claiming manifest prunes
	// a directory that is already gone, and an under-claiming one orphans
	// files nothing will ever remove.
	keep(writeManifest(u.manifestDir, u.before))
	return first
}
