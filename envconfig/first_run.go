package envconfig

import (
	"fmt"
	"os"
	"path/filepath"
)

const envFileTemplate = `# =====================================================================
# Ollama — environment reference (recreated by TUI → Service)
#
# Priority: process env  >  ~/.ollama/env  >  /etc/ollama/env
# This file overrides the system file, so it ships fully commented out:
# uncomment a line to activate it. Nothing here changes behaviour yet.
#
# Format: KEY=VALUE, one per line. Comments must be whole lines (no
# inline comments). Quotes around values are stripped.
# =====================================================================

# ---------------------------------------------------------------------
# SERVER — where ollama serve listens
# ---------------------------------------------------------------------

# Listen address. 0.0.0.0 = reachable from the LAN, 127.0.0.1 = local only.
# OLLAMA_BIND=0.0.0.0:11434

# Classic upstream variable (still honoured when OLLAMA_BIND is unset).
# OLLAMA_HOST=127.0.0.1:11434

# ---------------------------------------------------------------------
# CLIENT — where TUI/CLI connect to
# ---------------------------------------------------------------------

# Client target. Default: same as OLLAMA_BIND/OLLAMA_HOST.
# OLLAMA_URL=127.0.0.1:11434

# Allowed CORS origins for the web UI (comma separated).
# OLLAMA_ORIGINS=http://localhost:3000

# Desktop app: allow cross-origin requests from the bundled UI.
# OLLAMA_CORS=1

# Desktop app: override the ollama.com URL used by the UI.
# OLLAMA_DOT_COM_URL=https://ollama.com

# Desktop app: path to its local SQLite database.
# OLLAMA_APP_DB_PATH=/home/user/.ollama/app.db

# ---------------------------------------------------------------------
# MODELS AND STORAGE
# ---------------------------------------------------------------------

# Models directory. Default: ~/.ollama/models
# OLLAMA_MODELS=/usr/share/ollama/.ollama/models

# How long a model stays in VRAM after the last request. Default: 5m
# OLLAMA_KEEP_ALIVE=20m

# How many models may stay loaded at once. 0 = auto.
# OLLAMA_MAX_LOADED_MODELS=2

# Parallel requests per loaded model. Default: 1
# OLLAMA_NUM_PARALLEL=1

# Maximum queued requests. Default: 512
# OLLAMA_MAX_QUEUE=512

# Default context length when the model does not define one.
# 0 = pick 4k / 32k / 256k based on free VRAM.
# OLLAMA_CONTEXT_LENGTH=32768

# How long a model load may stall before giving up. Default: 5m
# OLLAMA_LOAD_TIMEOUT=5m

# Do not delete old blobs on startup.
# OLLAMA_NOPRUNE=1

# Parallel streams for safetensors pull/push. Default: 4
# OLLAMA_MAX_TRANSFER_STREAMS=4

# Allowed hosts for remote model pulls. Default: ollama.com
# OLLAMA_REMOTES=ollama.com,huggingface.co

# ---------------------------------------------------------------------
# GPU / VRAM
# ---------------------------------------------------------------------

# Flash Attention: faster, uses more VRAM. On by default on supported GPUs.
# OLLAMA_FLASH_ATTENTION=1

# K/V cache quantization: f16 (full), q8_0, q4_0 (less VRAM).
# OLLAMA_KV_CACHE_TYPE=q4_1

# Reserve VRAM per GPU, in bytes.
# OLLAMA_GPU_OVERHEAD=0

# Allow integrated GPUs to be used.
# OLLAMA_IGPU_ENABLE=0

# Enable the Vulkan backend.
# OLLAMA_VULKAN=1

# Force a specific llama.cpp build instead of autodetection.
# OLLAMA_LLM_LIBRARY=

# Restrict visible GPUs (CUDA / ROCm / Vulkan / AMD ordinal).
# CUDA_VISIBLE_DEVICES=0
# HIP_VISIBLE_DEVICES=0
# ROCR_VISIBLE_DEVICES=0
# GGML_VK_VISIBLE_DEVICES=0
# GPU_DEVICE_ORDINAL=0

# Pretend an unsupported AMD GPU is a known one (e.g. gfx1030).
# HSA_OVERRIDE_GFX_VERSION=10.3.0

# llama.cpp automatic memory fit (default "on") and its VRAM margin in MiB.
# LLAMA_ARG_FIT=on
# LLAMA_ARG_FIT_TARGET=1024

# ---------------------------------------------------------------------
# DEBUG AND DIAGNOSTICS
# ---------------------------------------------------------------------

# Log level: 0/false = info (default), 1/true = debug, 2 = trace.
# OLLAMA_DEBUG=1

# Dump inference request bodies and replayable curl commands to a temp dir.
# OLLAMA_DEBUG_LOG_REQUESTS=1

# Render Modelfile TEMPLATE (Go templates) when the model has one.
# OLLAMA_GO_TEMPLATE=1

# Server-side authentication between client and server.
# OLLAMA_AUTH=1

# Comma separated experimental feature switches.
# OLLAMA_EXPERIMENT=

# ---------------------------------------------------------------------
# CLOUD
# ---------------------------------------------------------------------

# Disable cloud features (remote inference, web search).
# OLLAMA_NO_CLOUD=1

# Override the cloud proxy base URL (testing / self-hosted proxy).
# OLLAMA_CLOUD_BASE_URL=

# ---------------------------------------------------------------------
# EXTERNAL PROVIDERS (providers.json)
# ---------------------------------------------------------------------

# Explicit path to providers.json. Default: $OLLAMA_HOME/providers.json
# or ~/.ollama/providers.json
# OLLAMA_PROVIDERS_CONFIG=/home/user/.ollama/providers.json

# Base directory for ollama-owned config (providers.json, prompt file).
# OLLAMA_HOME=/home/user/.ollama

# Replace the built-in system prompt with the one from this file.
# OLLAMA_EXTERNAL_SYSTEM_PROMPT=/home/user/.ollama/system-prompt.md

# Force model creation through the server API even when the server is local.
# OLLAMA_CREATE_REMOTE=1

# ---------------------------------------------------------------------
# TUI / AGENT (lama)
# ---------------------------------------------------------------------

# Default model for the lama quick start.
# LAMA_MODEL=lama

# Disable the agent's web_search / web_fetch tools.
# OLLAMA_AGENT_DISABLE_WEBSEARCH=1

# Disable the agent's shell tools (bash / powershell).
# OLLAMA_AGENT_DISABLE_SHELL=1

# Extra skills directory for the agent.
# OLLAMA_SKILLS=/home/user/.ollama/skills

# Editor used by Ctrl+G prompt editing.
# OLLAMA_EDITOR=vim

# Do not keep readline history.
# OLLAMA_NOHISTORY=1

# ---------------------------------------------------------------------
# SCHEDULING
# ---------------------------------------------------------------------

# Always spread a model across all GPUs.
# OLLAMA_SCHED_SPREAD=1

# ---------------------------------------------------------------------
# NETWORK / PROXY
# ---------------------------------------------------------------------

# HTTP proxy for model pulls behind a corporate firewall.
# Lowercase variants are honoured as well on Linux/macOS.
# HTTP_PROXY=http://proxy.local:3128
# HTTPS_PROXY=http://proxy.local:3128
# NO_PROXY=127.0.0.1,localhost
# http_proxy=http://proxy.local:3128
# https_proxy=http://proxy.local:3128
# no_proxy=127.0.0.1,localhost
`

// EnsureEnvFile creates /etc/ollama/env with full documentation if it doesn't
// exist yet. Non-fatal: on failure a warning is printed and false is returned.
func EnsureEnvFile() bool {
	path := filepath.Join("/etc", "ollama", "env")
	if _, err := doWriteEnvFileAt(path, false); err != nil {
		fmt.Fprintf(os.Stderr, "ollama: warning: %v\n", err)
		return false
	}
	return true
}

// RecreateEnvFile overwrites /etc/ollama/env with the full documented reference.
// Returns an error if writing fails.
func RecreateEnvFile() (string, error) {
	return doWriteEnvFileAt(filepath.Join("/etc", "ollama", "env"), true)
}

// UserEnvPath returns the path of the user-level env override file
// (~/.ollama/env), which takes priority over /etc/ollama/env.
func UserEnvPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home dir: %w", err)
	}
	return filepath.Join(home, ".ollama", "env"), nil
}

// RecreateUserEnvFile overwrites ~/.ollama/env with the full documented
// reference. Used from the TUI service section; needs no sudo.
func RecreateUserEnvFile() (string, error) {
	path, err := UserEnvPath()
	if err != nil {
		return "", err
	}
	return doWriteEnvFileAt(path, true)
}

// doWriteEnvFileAt writes the reference template to path. With force=false an
// existing file is left untouched (returns its path, nil).
func doWriteEnvFileAt(path string, force bool) (string, error) {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return path, nil // already exists, don't touch the user's config
		}
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("cannot create %s: %w", dir, err)
	}

	if err := os.WriteFile(path, []byte(envFileTemplate), 0644); err != nil {
		return "", fmt.Errorf("cannot write %s: %w", path, err)
	}

	if !force {
		fmt.Fprintf(os.Stderr, "ollama: created default config at %s (edit to customize)\n", path)
	}
	return path, nil
}
