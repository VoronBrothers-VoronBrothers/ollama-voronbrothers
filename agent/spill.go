package agent

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Spillover: when a tool result exceeds the threshold, write full content to
// a file and replace in-context content with a pointer. The model can then
// decide whether to read the file (partially or fully) via its file tools.
//
// Why: long bash outputs, large file reads, web extracts eat tokens. Spillover
// keeps only a preview + path in context; full text lives on disk.
//
// File location: ~/.ollama/cache/spill/{sha256[:16]}.txt
// Cleanup: TTL 24h (files older than that are removed on next spill).

const (
	// spillThresholdRunes is the rune count above which spillover activates.
	// ~15000 runes ≈ 4000 tokens — enough to be painful, small enough to preview.
	spillThresholdRunes = 15000

	// spillPreviewRunes is how many runes of head content remain in context.
	// Model sees the start of the output and can decide if it needs more.
	spillPreviewRunes = 2000

	// spillTailRunes is how many runes from the END stay in context. Tail matters:
	// logs typically end with errors/summaries, so head-only previews hide them.
	spillTailRunes = 1000

	// spillTTL is how long spill files are kept on disk.
	spillTTL = 24 * time.Hour
)

func spillDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, ".ollama", "cache", "spill")
	os.MkdirAll(dir, 0o755)
	return dir
}

// spillToolResult checks if content exceeds the spillover threshold. If so:
//   - writes full content to ~/.ollama/cache/spill/{hash}.txt
//   - returns a replacement string with preview + pointer
//
// If content is under threshold, returns it unchanged (no file written).
func spillToolResult(toolName, content string) string {
	runes := len([]rune(content))
	if runes <= spillThresholdRunes {
		return content
	}

	dir := spillDir()
	if dir == "" {
		// Can't determine home — fall back to normal truncation (no spill).
		return content
	}

	hash := sha256.Sum256([]byte(toolName + "\x00" + content))
	filename := fmt.Sprintf("%x.txt", hash[:8])
	filePath := filepath.Join(dir, filename)

	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		// Write failed — keep original content (will be truncated normally).
		return content
	}

	tokenEstimate := runes / 4 // rough: 1 token ≈ 4 runes for mixed text
	runesSlice := []rune(content)
	head := string(runesSlice[:spillPreviewRunes])
	tail := string(runesSlice[len(runesSlice)-spillTailRunes:])
	omitted := runes - spillPreviewRunes - spillTailRunes

	return fmt.Sprintf(
		"## Tool output spilled to file\n"+
			"Full output: %d tokens (≈%d chars), saved at:\n%s\n\n"+
			"Head (first %d chars):\n```\n%s...\n```\n\n"+
			"[%d chars omitted in the middle]\n\n"+
			"Tail (last %d chars):\n```\n...%s\n```\n\n"+
			"To read more, use the file tool `read` on that path with a start/end line range — fetch only the lines you need, not the whole file.",
		tokenEstimate, runes, filePath, spillPreviewRunes, head, omitted, spillTailRunes, tail,
	)
}

// cleanupSpillFiles removes spill files older than TTL. Called lazily on each
// new spill to avoid a separate goroutine/timer.
func cleanupSpillFiles() {
	dir := spillDir()
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-spillTTL)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
