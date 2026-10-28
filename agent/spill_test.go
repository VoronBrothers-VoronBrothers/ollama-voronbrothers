package agent

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// Demo test: shows exactly what the model sees after spillover and verifies
// the full content is preserved on disk. Run with: go test -run TestSpillDemo ./agent/ -v
func TestSpillDemo(t *testing.T) {
	// 1) Small output — passes through untouched (no file written).
	small := "line one\nline two"
	if got := spillToolResult("bash", small); got != small {
		t.Fatalf("small content must pass unchanged, got %q", got)
	}

	// 2) Large output (> 15000 runes) — spills to file.
	var b strings.Builder
	for i := 1; i <= 600; i++ {
		fmt.Fprintf(&b, "log line %d: some payload data for the model to inspect\n", i)
	}
	full := b.String()

	replacement := spillToolResult("bash", full)

	if !strings.Contains(replacement, "spilled to file") {
		t.Fatalf("expected spill replacement, got:\n%s", replacement)
	}
	// Head preview keeps the start; tail preview must surface the END of output.
	if !strings.Contains(replacement, "Head (first 2000 chars):") ||
		!strings.Contains(replacement, "log line 1:") {
		t.Fatalf("head preview missing:\n%s", replacement)
	}
	if !strings.Contains(replacement, "Tail (last 1000 chars):") ||
		!strings.Contains(replacement, "log line 600:") {
		t.Fatalf("tail preview must show the end of output:\n%s", replacement)
	}
	if !strings.Contains(replacement, "chars omitted in the middle") {
		t.Fatalf("omitted-in-middle marker missing:\n%s", replacement)
	}
	// Model is told to read partially, not the whole file.
	if !strings.Contains(replacement, "start/end line range") {
		t.Fatalf("range-read hint missing:\n%s", replacement)
	}
	// Find the saved path: line ending in .txt right after "saved at:".
	lines := strings.Split(replacement, "\n")
	var saved string
	for i, l := range lines {
		if strings.HasSuffix(strings.TrimSpace(l), ".txt") && i > 0 && strings.Contains(lines[i-1], "saved at:") {
			saved = l
			break
		}
	}
	if saved == "" {
		t.Fatalf("could not find saved path in:\n%s", replacement)
	}

	data, err := os.ReadFile(saved)
	if err != nil {
		t.Fatalf("spill file missing: %v (path=%s)", err, saved)
	}
	if string(data) != full {
		t.Fatalf("spill file must contain FULL content verbatim; got %d bytes want %d", len(data), len(full))
	}
	if !strings.HasPrefix(string(data), "log line 1:") || !strings.Contains(string(data), "log line 600:") {
		t.Fatalf("spill file head/tail corrupted")
	}

	// Show what the model actually receives in context.
	fmt.Println("\n========== WHAT THE MODEL SEES (in-context replacement) ==========")
	fmt.Println(replacement)
	fmt.Println("===================================================================")
	fmt.Printf("Full content on disk: %s (%d bytes, first and last line intact)\n", saved, len(data))
}
