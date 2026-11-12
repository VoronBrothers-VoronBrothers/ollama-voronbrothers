# Ollama Fork — Windows Build & Setup

## Cross-Compile (из Linux)

```bash
cd "/home/voron/Документы/VB-ollama/GitHub Ollama кастомный бинарник/ollama fork our fork"

GOOS=windows GOARCH=amd64 go build \
  -ldflags "-X github.com/ollama/ollama/version.Version=0.33.0-rc3-voron-win" \
  -o dist/ollama-voron-WIN_amd64.exe .
```

### Naming convention (как для Linux, с суффиксом платформы)

| OS | File | Version suffix |
|---|---|---|
| Linux amd64 | `ollama-voron-LNX_x86_64` | `0.33.0-rc3-voron` |
| Windows amd64 | `ollama-voron-WIN_amd64.exe` | `0.33.0-rc3-voron-win` |

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

### Option A: env file (RECOMMENDED — auto-applied, no script needed)

One-time setup in PowerShell:

```powershell
mkdir $env:USERPROFILE\.ollama -Force
@"
OLLAMA_HOST=192.168.0.9:14434
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
$env:OLLAMA_HOST = "192.168.0.9:14434"
.\ollama.exe
```

To check current value: `echo $env:OLLAMA_HOST`

### Option C: Permanent (registry, User-level)

```powershell
[Environment]::SetEnvironmentVariable("OLLAMA_HOST", "192.168.0.9:14434", "User")
```

Then any new PowerShell session has it set automatically.

## env file format

Same as Linux `/etc/ollama/env`:
- One `KEY=VALUE` per line
- Comments start with `#` (whole line)
- No inline comments, no spaces around `=`

Example:
```
OLLAMA_HOST=192.168.0.9:14434
OLLAMA_KEEP_ALIVE=20m
LAMA_MODEL=lama
OLLAMA_AGENT_DISABLE_WEBSEARCH=1
```

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
  [Environment]::SetEnvironmentVariable("OLLAMA_HOST", $null, "User")
  ```
