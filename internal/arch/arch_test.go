package arch

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"go/parser"
	"go/token"
)

// banned maps a package prefix to the prefixes it must never depend on,
// transitively. This is the dependency rule from docs/DESIGN.md:
//
//	model  <- leaf, imports nothing of ours
//	  ^
//	games  host      <- both import model only, never each other
//	  ^      ^
//	tasks  services
//	  ^
//	core
//	  ^
//	tui  ·  cmd      <- two peers, both consumers of core
//
// The rule that earns its keep is games not importing host: a game describes
// the container it wants and never builds one, which is what keeps plugins
// free of Docker and testable without it.
//
// Paths are module-relative. Test files are excluded: a test may legitimately
// import a fake from another layer, and constraining that buys nothing.
var banned = map[string][]string{
	"internal/model":    {"internal/", "cmd/"},
	"internal/games":    {"internal/host", "internal/core", "internal/tasks", "internal/services", "internal/tui", "cmd/"},
	"internal/host":     {"internal/games", "internal/core", "internal/tasks", "internal/services", "internal/tui", "cmd/"},
	"internal/tasks":    {"internal/core", "internal/tui", "cmd/"},
	"internal/services": {"internal/core", "internal/tui", "cmd/"},
	"internal/config":   {"internal/core", "internal/tui", "internal/host", "internal/games", "cmd/"},
	"internal/store":    {"internal/core", "internal/tui", "internal/games", "cmd/"},
	"internal/core":     {"internal/tui", "cmd/"},
	"internal/tui":      {"cmd/"},
}

// bannedDirect is checked against direct imports only.
//
// The tui rule has to be direct: a view reaching for a host type is the smell
// — one snapshot field short, and the next thing it reaches for is a call — but
// tui -> core -> host is correct and unavoidable, because the store is exactly
// the thing that translates what the engine reported into what a view renders.
// Stating this transitively would ban the layering the design asks for.
//
// tui may import games. Capability gating is a type assertion on a game, which
// is how the Mods view answers "Valheim has no mod system Garrison can
// manage" itself instead of
// leaking that knowledge into the shell.
var bannedDirect = map[string][]string{
	"internal/tui": {"internal/host"},
}

// mustFind names packages the walk has to discover. Without this the test
// would pass silently if the walk ever broke and found nothing.
var mustFind = []string{
	"cmd/garrison",
	"internal/core",
	"internal/games",
	"internal/host",
	"internal/host/docker",
	"internal/config",
	"internal/model",
	"internal/services/fleet",
	"internal/tasks",
	"internal/tui",
	"internal/tui/views/fleet",
}

func TestDependencyRule(t *testing.T) {
	root := moduleRoot(t)
	direct := parseGraph(t, root, modulePath(t, root))

	for _, want := range mustFind {
		if _, ok := direct[want]; !ok {
			t.Fatalf("walk did not find %s — the test is not checking anything", want)
		}
	}

	for _, v := range violations(closure(direct), banned) {
		t.Error(v)
	}
	for _, v := range violations(direct, bannedDirect) {
		t.Error(v)
	}
}

// TestRuleCatchesViolation checks the rule itself against a synthetic graph,
// so a passing TestDependencyRule means the repo is clean rather than the
// checker being broken.
func TestRuleCatchesViolation(t *testing.T) {
	got := violations(closure(map[string][]string{
		// games -> tui only via host: the rule must follow the chain.
		"internal/games": {"internal/host"},
		"internal/host":  {"internal/tui"},
		"internal/tui":   nil,
	}), banned)
	if len(got) == 0 {
		t.Fatal("expected the rule to reject internal/games -> internal/host -> internal/tui")
	}
}

// The direct rules need their own check, and they need the opposite property:
// they must fire on a direct import and stay silent on a transitive one.
func TestDirectRuleIgnoresTransitiveEdges(t *testing.T) {
	viewImportsHost := map[string][]string{
		"internal/tui/views/fleet": {"internal/host"},
	}
	if got := violations(viewImportsHost, bannedDirect); len(got) == 0 {
		t.Error("expected the rule to reject a view importing internal/host directly")
	}

	// The layering the design actually asks for must not trip it.
	viaCore := map[string][]string{
		"internal/tui/views/fleet": {"internal/core"},
		"internal/core":            {"internal/host"},
	}
	if got := violations(viaCore, bannedDirect); len(got) != 0 {
		t.Errorf("tui -> core -> host was rejected, but it is the intended layering: %v", got)
	}
}

// violations reports every edge in deps that rules forbids. The caller decides
// whether deps is the direct graph or its transitive closure.
func violations(deps map[string][]string, rules map[string][]string) []string {
	var out []string
	for pkg, ds := range deps {
		for prefix, forbidden := range rules {
			if !strings.HasPrefix(pkg, prefix) {
				continue
			}
			for _, d := range ds {
				for _, bad := range forbidden {
					if strings.HasPrefix(d, bad) {
						out = append(out, pkg+" must not depend on "+d+
							"\n\trule: nothing under "+prefix+" may reach "+bad)
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// closure expands a direct dependency graph into a transitive one.
func closure(direct map[string][]string) map[string][]string {
	out := make(map[string][]string, len(direct))
	for pkg := range direct {
		seen := map[string]bool{}
		var walk func(string)
		walk = func(p string) {
			for _, d := range direct[p] {
				if seen[d] {
					continue
				}
				seen[d] = true
				walk(d)
			}
		}
		walk(pkg)
		ds := make([]string, 0, len(seen))
		for d := range seen {
			ds = append(ds, d)
		}
		sort.Strings(ds)
		out[pkg] = ds
	}
	return out
}

// parseGraph reads every non-test .go file in the module and returns each
// package's direct dependencies on other packages in this module, keyed by
// module-relative path.
//
// It parses the sources itself rather than shelling out to `go list` so that
// go test's cache tracks the files it read — otherwise this test caches a pass
// and stops re-running when the packages it checks are the ones that changed.
func parseGraph(t *testing.T, root, mod string) map[string][]string {
	t.Helper()

	direct := map[string][]string{}
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		pkg := filepath.ToSlash(rel)
		if pkg == "." {
			pkg = ""
		}
		if _, ok := direct[pkg]; !ok {
			direct[pkg] = nil
		}

		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if !strings.HasPrefix(p, mod+"/") {
				continue // stdlib or third party: not our rule's business
			}
			direct[pkg] = append(direct[pkg], strings.TrimPrefix(p, mod+"/"))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return direct
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolving module root: %v", err)
	}
	return root
}

func modulePath(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatal("no module line in go.mod")
	return ""
}
