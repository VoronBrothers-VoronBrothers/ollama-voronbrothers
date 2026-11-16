package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ollama/ollama/api"
	"github.com/ollama/ollama/cmd/config"
)

// predsaveDir returns ~/.ollama/chats — the same directory /save uses.
func predsaveDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ollama", "chats")
}

// PredsaveDialogBeforeCompaction writes a /save-style JSON snapshot of the
// dialog to ~/.ollama/chats right before compaction (manual or automatic),
// gated by the global toggle (/predsave-compaction, default on).
// Returns the written file path, or "" if disabled/skipped.
func PredsaveDialogBeforeCompaction(model, systemPrompt string, messages []api.Message) string {
	if !config.PredsaveCompactionEnabled() {
		return ""
	}

	withSystem := make([]api.Message, 0, len(messages)+1)
	if strings.TrimSpace(systemPrompt) != "" {
		withSystem = append(withSystem, api.Message{Role: "system", Content: systemPrompt})
	}
	withSystem = append(withSystem, messages...)

	raw, err := json.MarshalIndent(api.ChatRequest{Model: model, Messages: withSystem}, "", "  ")
	if err != nil {
		return ""
	}

	name := fmt.Sprintf("chat-%s-predsave.json", time.Now().Format("2006-01-02-150405"))
	dir := predsaveDir()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ""
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		return ""
	}
	return path
}
