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
	"internal/core":     {"internal/tui", "cmd/"},
}

// mustFind names packages the walk has to discover. Without this the test
// would pass silently if the walk ever broke and found nothing.
var mustFind = []string{"cmd/garrison", "internal/games", "internal/host", "internal/model"}

func TestDependencyRule(t *testing.T) {
	root := moduleRoot(t)
	direct := parseGraph(t, root, modulePath(t, root))

	for _, want := range mustFind {
		if _, ok := direct[want]; !ok {
			t.Fatalf("walk did not find %s — the test is not checking anything", want)
		}
	}

	for _, v := range violations(closure(direct)) {
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
	}))
	if len(got) == 0 {
		t.Fatal("expected the rule to reject internal/games -> internal/host -> internal/tui")
	}
}

// violations reports every banned edge in a transitive dependency graph.
func violations(deps map[string][]string) []string {
	var out []string
	for pkg, ds := range deps {
		for prefix, forbidden := range banned {
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
