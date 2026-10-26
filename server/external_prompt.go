package server

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// [voron] Universal system prompt for external API provider models.
//
// It lives next to providers.json so a single directory holds all external
// provider configuration:
//
//	~/.ollama/external_system_prompt.md   (preferred)
//	~/.ollama/system_prompt.md            (accepted alias)
//
// The prompt is applied ONLY to models served by external providers
// ("provider/vendor/model"); locally run models are never affected.
//
// Two injection points use it:
//   - ShowHandler: external models report it as ShowResponse.System, so the
//     chat/agent TUIs include it in the prompt they build and display;
//   - buildOpenAIRequest: raw /api/chat clients that send their own messages
//     still get the prompt prepended to the outgoing OpenAI request.
//
// The file is re-read when its mtime/size changes, so edits apply without a
// daemon restart.

// externalPromptEnvName optionally points at an explicit prompt file. Like
// OLLAMA_PROVIDERS_CONFIG it is authoritative: when set, no other path is
// consulted (and tests use it to stay hermetic).
const externalPromptEnvName = "OLLAMA_EXTERNAL_SYSTEM_PROMPT"

// externalPromptFileNames are the file names looked up inside the config dir.
var externalPromptFileNames = []string{"external_system_prompt.md", "system_prompt.md"}

// externalPromptCache memoises the last successfully read file, keyed by
// path/mtime/size, so per-request loading stays cheap.
type externalPromptCacheEntry struct {
	path    string
	modTime time.Time
	size    int64
	text    string
}

var (
	externalPromptMu    sync.Mutex
	externalPromptCache externalPromptCacheEntry
)

// externalPromptConfigDir mirrors providerConfigPaths' config directory logic.
//
// The prompt deliberately follows providers.json: the daemon runs as user
// "ollama" with a different $HOME, so ~/.ollama is usually invisible to it.
// When OLLAMA_PROVIDERS_CONFIG points into another user's home (as our systemd
// drop-in does), the prompt is looked up in that same directory — no extra
// env var or drop-in needed.
func externalPromptConfigDir() string {
	if explicit := strings.TrimSpace(os.Getenv(providersEnvName)); explicit != "" {
		return filepath.Dir(filepath.Clean(explicit))
	}
	if dir := strings.TrimSpace(os.Getenv("OLLAMA_HOME")); dir != "" {
		return filepath.Clean(dir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		slog.Warn("external prompt: cannot determine home directory", "error", err)
		return ""
	}
	return filepath.Join(home, ".ollama")
}

// externalSystemPromptPaths returns candidate prompt paths in priority order.
func externalSystemPromptPaths() []string {
	if explicit := strings.TrimSpace(os.Getenv(externalPromptEnvName)); explicit != "" {
		return []string{filepath.Clean(explicit)}
	}

	dir := externalPromptConfigDir()
	if dir == "" {
		return nil
	}
	paths := make([]string, 0, len(externalPromptFileNames))
	for _, name := range externalPromptFileNames {
		paths = append(paths, filepath.Join(dir, name))
	}
	return paths
}

// externalSystemPrompt returns the universal prompt for external models, or ""
// when no usable file is configured.
func externalSystemPrompt() string {
	paths := externalSystemPromptPaths()
	if len(paths) == 0 {
		return ""
	}

	externalPromptMu.Lock()
	defer externalPromptMu.Unlock()

	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			if !os.IsNotExist(err) {
				slog.Warn("external prompt: cannot stat file", "path", path, "error", err)
			}
			continue
		}
		if info.IsDir() {
			slog.Warn("external prompt: path is a directory", "path", path)
			continue
		}

		if externalPromptCache.path == path &&
			externalPromptCache.size == info.Size() &&
			externalPromptCache.modTime.Equal(info.ModTime()) {
			return externalPromptCache.text
		}

		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("external prompt: cannot read file", "path", path, "error", err)
			continue
		}

		text := strings.TrimSpace(string(data))
		if text == "" {
			slog.Warn("external prompt: file is empty", "path", path)
			text = ""
		}
		slog.Debug("external prompt: loaded", "path", path, "bytes", len(text))

		externalPromptCache = externalPromptCacheEntry{
			path:    path,
			modTime: info.ModTime(),
			size:    info.Size(),
			text:    text,
		}
		return text
	}

	return ""
}

// prependExternalSystemPrompt inserts the universal prompt into an outgoing
// OpenAI message list. When the caller already sent a system message that
// contains the prompt (the TUI does, via ShowResponse.System) nothing is
// added, so the prompt is never duplicated.
func prependExternalSystemPrompt(messages []openaiMessage, prompt string) []openaiMessage {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return messages
	}

	if len(messages) > 0 && messages[0].Role == "system" {
		if strings.Contains(messages[0].Content, prompt) {
			return messages
		}
		merged := make([]openaiMessage, 0, len(messages))
		first := messages[0]
		first.Content = prompt + "\n\n" + strings.TrimSpace(first.Content)
		merged = append(merged, first)
		return append(merged, messages[1:]...)
	}

	out := make([]openaiMessage, 0, len(messages)+1)
	out = append(out, openaiMessage{Role: "system", Content: prompt})
	return append(out, messages...)
}
