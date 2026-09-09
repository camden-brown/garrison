# 0001 — Native Windows binary; only the game servers run in Docker

**Status:** Accepted · 2026-09-09

## Context

The game servers run as Docker containers. The natural next thought is to
containerize Garrison itself, mount the Docker socket, and run everything the
same way. That thought was prompted partly by a wrong belief that Go is poorly
supported on Windows.

Go is fully supported on Windows. `go build` produces a native `.exe` with no
runtime, no VM and no container. The only narrow gap is Go's `plugin` package
(see [0002](0002-compile-time-game-plugins.md)).

## Decision

Garrison is a native Windows executable. It reaches Docker over
`npipe:////./pipe/docker_engine`, with the endpoint configurable. Only the game
servers are containerized.

## Consequences

- The dashboard survives what it watches. A containerized dashboard vanishes
  when the daemon restarts after a Docker Desktop update or when a container
  storm is eating the machine — the moments it exists for.
- Windows integration keeps working: clipboard yank, secrets in Credential
  Manager, `garrison restart …` from Task Scheduler, launch on login.
- Iteration is `go build` and run, not rebuild-image and restart-container.
- Linux containers still run in a Linux VM via Docker Desktop's WSL2 backend
  whether we like it or not, which is why bind mounts from `D:\` are slow. That
  cost is real but unrelated to where Garrison itself runs.

## Rejected

**Containerize the TUI.** Would have unlocked Go's runtime `plugin` loading —
which we do not need — at the cost of everything above, plus `docker run -it`
with a socket mount on every launch.

**A container as a build step** is *not* rejected and is documented in the
README: it produces a native `.exe` without installing the Go toolchain. That
is a different thing from running the tool in one.

## How we would know this was wrong

If Garrison ever needs to run headless on a Linux host as a long-lived daemon
with a web UI, it is a different product and this decision does not apply to
it. Short of that, nothing here is likely to flip.
