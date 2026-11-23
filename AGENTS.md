# AGENTS.md

> **Voron custom build: read `VORON-RUNBOOK.md` before any work in this repo.**
> It covers: tool registration, build & deploy (ldflags, atomic swap), env flags,
> and the exact cycle for adding new tools.

## llama.cpp selection (IMPORTANT for agents)

**Runtime llama.cpp is NOT the `LLAMA_CPP_VERSION` file.** The active build is
chosen by the env var **`OLLAMA_WHERE_IS_MY_LLAMA_CPP`** — it points to a flat
directory with `llama-server` + `libggml*.so` (e.g. `/home/voron/.ollama/llama-b12651`).

- Where the var is read: `ml/path.go`. Priority:
  **`~/.ollama/env` > process env (`/etc/ollama/env` via systemd) > auto-detect**
  (default `/usr/local/lib/ollama`).
- The user switches llama.cpp builds actively — always check the var's current
  value (`grep LLAMA /home/voron/.ollama/env /etc/ollama/env`) before assuming
  which build is active. Full switch procedure: `VORON-RUNBOOK.md §8`.
- **`LLAMA_CPP_VERSION` (repo root) is ONLY a CMake FetchContent pin** for
  source builds (`cmake -B build .`, `llama/server/CMakeLists.txt`).
  `scripts/voron-build.sh` does NOT read it. Before any CMake source build,
  sync the file to match the active llama.cpp build; do not treat its value as
  the truth about what is running.

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
