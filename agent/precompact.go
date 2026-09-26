package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Pre-compaction constants controlling the notes file and pushback behavior.
const (
	// MaxCompactionPushbacks is how many times per session the model can delay
	// compaction before it becomes forced.
	MaxCompactionPushbacks = 2

	// ForcedCompactionThreshold is the context fill ratio at which compaction
	// becomes mandatory regardless of pushback state. Above this fraction of
	// the context window, pre-compaction notes are skipped and compaction runs
	// immediately.
	ForcedCompactionThreshold float64 = 0.95

	// CompactNotesMaxBytes is the maximum size in bytes allowed for a single
	// compact-notes file write. The model should keep its notes concise;
	// anything larger gets truncated on read-back.
	CompactNotesMaxBytes int = 2048

	// MaxPreCompactRounds bounds how many inline model rounds the pre-compact
	// guard gives the model to actually persist its notes before compaction
	// proceeds. A small model may announce "I'll save these now" without
	// emitting the write tool call on that turn; rather than compacting on an
	// empty round we nudge it once or twice, then always proceed.
	MaxPreCompactRounds = 3
)

// CompactionPushbackMarker is what the model includes in its response to
// indicate it wants to delay compaction this cycle.
const CompactionPushbackMarker = "[[COMPACT_PUSHBACK]]"

// CompactionReadyMarker signals the model considers itself ready for compaction.
const CompactionReadyMarker = "[[COMPACT_READY]]"

// CompactNotesPath returns the per-chat notes file path under /tmp where the
// model saves important state before compaction compresses context.
func CompactNotesPath(chatID string) string {
	safe := chatID
	if safe == "" {
		safe = "default"
	}
	// Sanitize to a short filesystem-safe suffix.
	var buf strings.Builder
	for _, r := range safe {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-':
			buf.WriteRune(r)
		default:
			buf.WriteRune('_')
		}
	}
	return filepath.Join(os.TempDir(), "ollama_compact_notes_"+buf.String()+".txt")
}

// compactNotesInstruction builds the user-facing message injected before a
// pre-compaction model round. It tells the model what to preserve and where to
// write it.
func compactNotesInstruction(notesPath string, fillPercent int) string {
	return fmt.Sprintf(
		"[System: Context is at %d%% of the window limit. "+
			"Save your most important current state (task progress, key decisions, files touched, unresolved items) to this file: %s " +
			"(use the write tool with path and content arguments; or bash). " +
			"The file will be overwritten each time; keep it under 2KB. "+
			"After writing (or deciding nothing needs saving), reply with [[COMPACT_READY]]. "+
			"If you strongly prefer to delay compaction one more turn, reply with [[COMPACT_PUSHBACK]] instead.",
		fillPercent, notesPath)
}

// checkCompactPushback inspects the last assistant response for the pushback
// or ready marker and reports which was found.
func checkCompactPushback(assistantContent string) (pushedBack bool, foundMarker bool) {
	content := strings.TrimSpace(assistantContent)
	if strings.Contains(content, CompactionPushbackMarker) {
		return true, true
	}
	if strings.Contains(content, CompactionReadyMarker) {
		return false, true
	}
	// No explicit marker: treat as ready (default to proceeding with compaction).
	return false, false
}
