# Decision records

Short records of decisions that are **expensive to reverse**, written so a
future session — human or agent — can tell a deliberate choice from an
accident, and knows what was rejected and why.

Each record states how we would know it was wrong. That matters more than the
decision: a rule nobody can falsify is dogma, and dogma is what gets worked
around instead of revisited.

Not everything belongs here. Keymaps, pane arrangement, tile contents and
colour choices are meant to be argued with and live in
[`../DESIGN.md`](../DESIGN.md).

| # | Decision | Status |
| :-: | --- | --- |
| [0001](0001-native-binary-servers-in-docker.md) | Native Windows binary; only the game servers run in Docker | Accepted |
| [0002](0002-compile-time-game-plugins.md) | Game support is compile-time packages, not runtime plugins | Accepted |
| [0003](0003-model-is-a-leaf-package.md) | `model` is a leaf package; `games` and `host` never import each other | Accepted |
| [0004](0004-single-writer-store.md) | Exactly one writer, in `internal/core` | Accepted |
| [0005](0005-arch-test-parses-sources.md) | The dependency rule is enforced by parsing sources, not `go list` | Accepted |
| [0006](0006-valheim-first.md) | Valheim is the first game implemented | Accepted |
| [0007](0007-services-and-store-meet-in-cmd.md) | Services and the store meet through interfaces, wired in `cmd` | Accepted |
| [0008](0008-game-vocabularies-come-from-the-game.md) | A plugin's option lists are extracted from the game, not transcribed | Accepted |
| [0009](0009-secrets-stay-in-the-config-file-for-now.md) | Secrets stay in the config file, and the form will not type one | Accepted |
| [0010](0010-the-console-ring-shares-its-storage.md) | The console ring shares storage between snapshots | Accepted |
| [0011](0011-compile-writes-whole-files.md) | `Compile` writes whole files, and `Plan` must agree with it | Accepted |
