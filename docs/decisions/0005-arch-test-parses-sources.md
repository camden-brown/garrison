# 0005 — The dependency rule is enforced by parsing sources, not `go list`

**Status:** Accepted · 2026-09-09

## Context

The dependency rule ([0003](0003-model-is-a-leaf-package.md)) is only worth
having if it is enforced. The first implementation shelled out to
`go list -json ./...` and checked each package's `Deps`.

It was verified by deliberately adding a `games` → `host` import. **The test
passed.** `go test` had cached the result: the cache tracks files the test
process reads, and a subprocess's reads are invisible to it. The test would
have stopped protecting anything exactly when the packages it checks were the
ones that changed — the failure mode where you believe you are covered and are
not.

## Decision

`internal/arch` walks the module with `filepath.WalkDir`, parses each non-test
file with `go/parser` in `ImportsOnly` mode, builds the transitive internal
dependency graph itself, and checks the rule against it. Because the test
process opens every source file, `go test`'s cache invalidates correctly.

A second test, `TestRuleCatchesViolation`, runs the rule against a synthetic
graph, so a green `TestDependencyRule` means the repo is clean rather than the
checker being broken.

## Consequences

- Stdlib only; no `golang.org/x/tools` dependency and no subprocess.
- Verified in both directions: clean repo passes; an injected `games` → `host`
  import fails with the offending edge and the rule it broke.
- Test files are excluded — a test may legitimately import a fake from another
  layer, and constraining that buys nothing.
- The graph walk is ours to maintain. It is about 80 lines and only needs to
  understand imports.

## Rejected

**`go list` with `-count=1` in CI.** Fixes caching only where someone remembers
the flag, and leaves local runs silently wrong.

**`golang.org/x/tools/go/packages`.** Correct and heavier; adds a dependency to
a project whose dependency list is deliberately short.

**A lint rule via `depguard`/`golangci-lint`.** Another toolchain to install
and pin for something a test does in stdlib.

## How we would know this was wrong

If the hand-rolled walk mishandles a real import form — build tags selecting
different imports per platform is the plausible one, since `ImportsOnly`
parsing ignores build constraints. If that starts mattering, move to
`go/packages` rather than back to `go list`.
