package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ollama/ollama/api"
)

// dialogLogLine is one JSONL record in the continuous dialog log.
type dialogLogLine struct {
	TS  string      `json:"ts"`
	Msg api.Message `json:"msg"`
}

// dialogLogger appends every message it has not seen before to an append-only
// JSONL file (~/.ollama/chats/chat-<ts>-dialog.jsonl). It gives a cumulative
// record of the whole dialog: unlike the pre-compaction snapshot, which only
// captures whatever is in memory at that moment, this log keeps every message
// ever sent to the model — including parts later replaced by compaction.
type dialogLogger struct {
	mu   sync.Mutex
	path string
	file *os.File

	seen map[string]bool
}

func newDialogLogger() (*dialogLogger, error) {
	name := fmt.Sprintf("chat-%s-dialog.jsonl", time.Now().Format("2006-01-02-150405"))
	dir := predsaveDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &dialogLogger{path: f.Name(), file: f, seen: make(map[string]bool)}, nil
}

func (d *dialogLogger) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.file == nil {
		return nil
	}
	err := d.file.Close()
	d.file = nil
	return err
}

// Append records every message from messages that has not been seen before.
// It returns the number of newly written lines.
func (d *dialogLogger) Append(messages []api.Message) int {
	if len(messages) == 0 {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.file == nil {
		return 0
	}

	written := 0
	buf := make([]byte, 0, 512)
	for _, msg := range messages {
		key := messageLogKey(msg)
		if d.seen[key] {
			continue
		}
		d.seen[key] = true

		line, err := json.Marshal(dialogLogLine{TS: time.Now().Format(time.RFC3339), Msg: msg})
		if err != nil {
			continue
		}
		buf = buf[:0]
		buf = append(buf, line...)
		buf = append(buf, '\n')
		if _, err := d.file.Write(buf); err == nil {
			written++
		}
	}
	return written
}

func (d *dialogLogger) Path() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.path
}

// messageLogKey is a stable identity for dedup: the same content sent again in
// a later run (or kept after compaction) must not be logged twice.
func messageLogKey(msg api.Message) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%d|", msg.Role, len(msg.Content))
	for _, tc := range msg.ToolCalls {
		fmt.Fprintf(h, "tc:%s:%s:", tc.ID, tc.Function.Name)
	}
	h.Write([]byte(msg.Content))
	return hex.EncodeToString(h.Sum(nil))
}
