# 0010 — The console ring shares storage between snapshots

**Status:** Accepted · 2026-09-09

## Context

Snapshots are immutable and published on every mutation. Everything else in one
is small enough to copy: a fleet is tens of servers, `model.History` is three
hundred points a tier. So the idiom throughout `internal/core` is copy-on-write,
and `appendBounded` copies the whole slice on every append.

The console does not fit that idiom. DESIGN asks for sixteen thousand lines per
server — four hours of a chatty world — and a crash-looping mod can emit
thousands of lines a second. Copying 16k events per line is a render loop spent
in `memmove`, which is why the console shipped at M1 as a 200-line tail with a
note saying the real ring would arrive with the view that needed it. That view
is now here.

## Decision

`model.Ring` shares one backing array across snapshots and appends into it in
place. Each snapshot holds its own length; an older one keeps reading the prefix
it always had. The array is allocated at twice the bound, and when it fills, the
newest `cap` events are copied into a fresh array — so the O(n) cost is paid
once per `cap` appends rather than once per append.

## Consequences

- The console holds `model.ConsoleCap` (16384) lines per server at roughly the
  cost the 200-line tail used to have.
- **This is only safe because the store has exactly one writer** ([ADR
  0004](0004-single-writer-store.md)). Mutations apply in sequence, so every
  `Add` operates on the newest ring and writes at an index no published snapshot
  can address. Two rings appending at the same index would corrupt both. If the
  single-writer rule is ever relaxed, this type breaks first and silently.
- `Ring.Events()` aliases the ring's storage. It is documented read-only, and
  appending to the returned slice would write into the array the next `Add` is
  going to use.
- Views can no longer walk the whole buffer per frame. The dashboard's tail and
  the fleet's activity feed take bounded windows (`Tail`), with the window size
  named and commented at each call site.

## Rejected

**Keep copying, with a smaller bound.** What M1 did, and it is why the console
was a 200-line tail rather than a console. The bound was chosen by what copying
could afford rather than by what an operator needs to see, which is the wrong
way round.

**A real circular buffer with a head index.** The natural shape for a ring, and
it cannot be shared between immutable snapshots: a wrapped write mutates a cell
an older snapshot is still reading. It would need a copy per snapshot, which is
the thing being avoided.

**Move the console out of the snapshot entirely** — a separate store the view
reads directly. It breaks the rule that a view renders a snapshot and nothing
else, and it puts a second concurrency story next to the one that already works.

## How we would know this was wrong

If a data race ever appears involving console events, this is the first place to
look, and `TestRingSnapshotsAreUnaffectedByLaterWrites` is the test that should
have caught it. More likely: if a second writer is ever added to the store for
an unrelated reason, this decision becomes wrong without anything failing
loudly — which is the case the ADR exists to warn about.
