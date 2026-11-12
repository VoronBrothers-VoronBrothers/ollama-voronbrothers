# Ollama Fork — Windows Build & Setup

## Cross-Compile (из Linux)

Нужен CGO (go-sqlite3) и mingw-кросс-компилятор: `sudo apt install gcc-mingw-w64-x86-64`.

```bash
cd "/home/voron/Документы/VB-ollama/GitHub Ollama кастомный бинарник/ollama fork our fork"
V=$(ollama --version | grep -oE '[0-9]+\.[0-9]+\.[0-9]+-voron-[0-9]{8}')   # не ниже текущей

CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=x86_64-w64-mingw32-gcc \
  go build -tags sqlite_fts5 \
  -ldflags "-X github.com/ollama/ollama/version.Version=${V}-win" \
  -o dist/ollama-voron-WIN_amd64.exe .
```

> Без `CC=…` сборка падает на `mlx/nn.go: undefined: Array` и
> `mlxrunner/xgrammar: build constraints exclude all Go files` — это не ошибка кода,
> а отсутствие CGO-компилятора для целевой платформы. Без `-tags sqlite_fts5`
> не работает RAG-индекс (`internal/knowledge`), т.е. tool `knowledge_search`.

### Naming convention (как для Linux, с суффиксом платформы)

| OS | File | Version suffix |
|---|---|---|
| Linux amd64 | `ollama-voron-LNX_x86_64` | `<версия>-voron-<DDMMYYYY>` |
| Windows amd64 | `ollama-voron-WIN_amd64.exe` | `<версия>-voron-<DDMMYYYY>-win` |

## Deploy to .13 (Windows)

Copy the `.exe` into `_bee_setup/` folder on the Windows machine:

```
C:\Users\<user>\_bee_setup\
└── ollama.exe          ← renamed from dist/ollama-voron-WIN_amd64.exe
```

## Setup on Windows — Env Variables

### How it works (same as Linux)

The Go binary reads a file called **`env`** (no extension!) at startup:

| Priority | Path (Windows) | Path (Linux) |
|---|---|---|
| 1 (highest) | Process `$env:` variables | `os.Getenv()` |
| 2 | `%USERPROFILE%\.ollama\env` | `~/.ollama/env` |
| 3 (lowest) | — (no system dir on Win) | `/etc/ollama/env` |

**Later files overwrite earlier ones.** So user file beats system file.

### Which variable to use: BIND vs URL vs HOST

Since the bind/client split (see `envconfig/client_url.go`):

| Variable | Meaning | Example |
|---|---|---|
| `OLLAMA_URL` | where the **client** (TUI/CLI/API) connects | `OLLAMA_URL=192.168.0.9:14434` |
| `OLLAMA_BIND` | where `ollama serve` **listens** | `OLLAMA_BIND=0.0.0.0:11434` |
| `OLLAMA_HOST` | legacy: used for both, when the two above are unset | `OLLAMA_HOST=192.168.0.9:14434` |

Rule of thumb on a client-only machine (like .13): set **`OLLAMA_URL`**.
On the server (the Linux box): set **`OLLAMA_BIND`** if it must listen on the LAN.
`OLLAMA_HOST` still works — it is the fallback for both — but the split variables
are clearer and let a client and a server share one config file.

### Option A: env file (RECOMMENDED — auto-applied, no script needed)

One-time setup in PowerShell:

```powershell
mkdir $env:USERPROFILE\.ollama -Force
@"
# client target (server on the LAN)
OLLAMA_URL=192.168.0.9:14434
OLLAMA_KEEP_ALIVE=20m
LAMA_MODEL=lama
"@ | Set-Content "$env:USERPROFILE\.ollama\env"
```

After this, just run `.\ollama.exe` — vars are read automatically by the binary.
**No wrapper script needed.**

To verify what's loaded:
```powershell
Get-Content "$env:USERPROFILE\.ollama\env"
```

### Option B: Session-only via PowerShell (no file)

Quick switch server for one session:

```powershell
$env:OLLAMA_URL = "192.168.0.9:14434"
.\ollama.exe
```

To check current value: `echo $env:OLLAMA_URL`

### Option C: Permanent (registry, User-level)

```powershell
[Environment]::SetEnvironmentVariable("OLLAMA_URL", "192.168.0.9:14434", "User")
```

Then any new PowerShell session has it set automatically.

## env file format

Same as Linux `/etc/ollama/env`:
- One `KEY=VALUE` per line
- Comments start with `#` (whole line)
- No inline comments, no spaces around `=`

Example:
```
# client target — the server on the LAN
OLLAMA_URL=192.168.0.9:14434
OLLAMA_KEEP_ALIVE=20m
LAMA_MODEL=lama
OLLAMA_AGENT_DISABLE_WEBSEARCH=1

# server-only (has no effect on a client machine):
# OLLAMA_BIND=0.0.0.0:11434
```

A full commented-out reference for every variable can be generated on Linux
via TUI → **Service** → *Recreate env vars (~/.ollama/env)*, or on Windows by
copying that file to `%USERPROFILE%\.ollama\env`.

## Tools available on Windows build

| Tool | Name in TUI | Works? |
|---|---|---|
| Shell (short, 40s) | `powershell` | ✅ |
| Shell (long, 60-400s) | `powershell_long` | ✅ |
| Read / Write / Edit files | `read`, `write`, `edit` | ✅ |
| View image | `view_image` | ✅ (if model supports) |
| Web search/fetch | `web_search`, `web_fetch` | ✅ (unless disabled in env) |
| Knowledge search | `knowledge_search` | ✅ |
| Skill loader | `skill` | ✅ (if skills catalog non-empty) |

> **Note:** On Windows, the "bash" tool is automatically named "powershell" 
> and executes via `powershell.exe -NoLogo -NonInteractive`. 
> All shell commands should use PowerShell syntax.

## Rollback

- Remove binary: delete `ollama.exe` from `_bee_setup/`
- Remove env file: `Remove-Item "$env:USERPROFILE\.ollama\env"`
- Remove registry vars (if Option C used):
  ```powershell
  [Environment]::SetEnvironmentVariable("OLLAMA_URL", $null, "User")
  ```
