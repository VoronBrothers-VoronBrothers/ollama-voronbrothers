# AGENTS.md

> **Voron custom build: read `VORON-RUNBOOK.md` before any work in this repo.**
> It covers: tool registration, build & deploy (ldflags, atomic swap), env flags,
> and the exact cycle for adding new tools.

## Building

> **Deploy builds (into /usr/local/bin) MUST go through `scripts/voron-build.sh`.**
> It does version determination, builds BOTH binaries (`ollama` + `lama`) with
> `-tags sqlite_fts5` and ldflags, backs up old files and swaps them atomically.
> Never run a bare `go build` for deployment — full rules: `VORON-RUNBOOK.md §3`.

For a full dev build from the repository root (no install):

```sh
cmake -B build .
cmake --build build --parallel 8
./ollama serve
```

For quick Go-only iteration against an existing native payload:

```sh
go build .
go run . serve
```

See `docs/development.md` for prerequisites, platform notes, GPU backends, and
the full development workflow.
