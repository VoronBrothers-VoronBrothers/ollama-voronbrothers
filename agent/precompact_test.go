package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain clears any leftover pre-compaction notes before the suite runs.
// injectPostCompactionReminder reads CompactNotesPath from os.TempDir(), so a
// stray /tmp/ollama_compact_notes_*.txt left by an earlier e2e/manual run would
// otherwise leak into unit tests (e.g. TestSessionCompactsAfterToolResultsBeforeContinuing)
// and flip their expected message lists. Removing the glob keeps the suite
// deterministic without touching production behavior.
func TestMain(m *testing.M) {
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "ollama_compact_notes_*.txt"))
	for _, path := range matches {
		_ = os.Remove(path)
	}
	os.Exit(m.Run())
}
