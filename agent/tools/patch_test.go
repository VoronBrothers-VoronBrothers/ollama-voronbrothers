package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ollama/ollama/agent"
)

func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func runPatch(t *testing.T, dir, patch string) (string, error) {
	t.Helper()
	res, err := (&Patch{}).Execute(context.Background(), agent.ToolContext{WorkingDir: dir}, map[string]any{"patch": patch})
	if err != nil {
		return res.Content, err
	}
	return res.Content, nil
}

func TestPatchUpdateWithContext(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "main.go", "package main\n\nfunc main() {\nold := 1\n}\n")

	res, err := runPatch(t, dir, `*** Begin Patch
*** Update File: main.go
@@ func main() {
-old := 1
+new := 2
*** End Patch`)
	if err != nil {
		t.Fatalf("patch failed: %v", err)
	}
	want := "package main\n\nfunc main() {\nnew := 2\n}\n"
	if got := readTestFile(t, dir, "main.go"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if !strings.Contains(res, "Updated") {
		t.Errorf("unexpected result message: %s", res)
	}
}

func TestPatchFuzzyMatch(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "cfg.txt", "alpha \t\nbeta\n")

	// Trailing tab in the file; pattern has none -> exact fails, trim-right level matches.
	if _, err := runPatch(t, dir, `*** Begin Patch
*** Update File: cfg.txt
-alpha
+ALPHA
*** End Patch`); err != nil {
		t.Fatalf("patch failed: %v", err)
	}
	if got := readTestFile(t, dir, "cfg.txt"); got != "ALPHA\nbeta\n" {
		t.Errorf("got %q, want \"ALPHA\\nbeta\\n\"", got)
	}

	// Whitespace on both sides -> trim-both level matches.
	writeTestFile(t, dir, "cfg2.txt", "  gamma  \n")
	if _, err := runPatch(t, dir, `*** Begin Patch
*** Update File: cfg2.txt
-gamma
+GAMMA
*** End Patch`); err != nil {
		t.Fatalf("patch failed: %v", err)
	}
	if got := readTestFile(t, dir, "cfg2.txt"); got != "GAMMA\n" {
		t.Errorf("got %q, want \"GAMMA\\n\"", got)
	}
}

func TestPatchMissingContextFails(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "f.txt", "one\ntwo\n")
	if _, err := runPatch(t, dir, `*** Begin Patch
*** Update File: f.txt
@@ no such line
-one
+ONE
*** End Patch`); err == nil {
		t.Fatal("expected error for missing context, got success")
	} else if !strings.Contains(err.Error(), "context") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPatchInsertAtEnd(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "f.txt", "a\nb\n")

	if _, err := runPatch(t, dir, `*** Begin Patch
*** Update File: f.txt
+appended line
*** End Patch`); err != nil {
		t.Fatalf("patch failed: %v", err)
	}
	if got := readTestFile(t, dir, "f.txt"); got != "a\nb\nappended line\n" {
		t.Errorf("got %q, want \"a\\nb\\nappended line\\n\"", got)
	}
}

func TestPatchTrailingEmptySentinel(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "f.txt", "a\nb") // no trailing newline

	if _, err := runPatch(t, dir, `*** Begin Patch
*** Update File: f.txt
-b
-
+B
+
*** End Patch`); err != nil {
		t.Fatalf("patch failed: %v", err)
	}
	// Sentinel retry: pattern ["b",""] not found; retried as ["b"], newSlice ["B",""].
	if got := readTestFile(t, dir, "f.txt"); got != "a\nB\n" {
		t.Errorf("got %q, want \"a\\nB\\n\"", got)
	}
}

func TestPatchEndOfFileChunk(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "f.txt", "x\ny\nx\n")

	if _, err := runPatch(t, dir, `*** Begin Patch
*** Update File: f.txt
-x
+x2
*** End of File
*** End Patch`); err != nil {
		t.Fatalf("patch failed: %v", err)
	}
	if got := readTestFile(t, dir, "f.txt"); got != "x\ny\nx2\n" {
		t.Errorf("got %q, want \"x\\ny\\nx2\\n\"", got)
	}
}

func TestPatchAddDeleteMove(t *testing.T) {
	dir := t.TempDir()
	if _, err := runPatch(t, dir, `*** Begin Patch
*** Add File: sub/dir/new.txt
+line one
+line two
*** End Patch`); err != nil {
		t.Fatalf("add failed: %v", err)
	}
	if got := readTestFile(t, dir, "sub/dir/new.txt"); got != "line one\nline two\n" {
		t.Errorf("created file content: got %q", got)
	}

	if _, err := runPatch(t, dir, `*** Begin Patch
*** Add File: sub/dir/new.txt
+x
*** End Patch`); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected 'already exists' error for re-add, got %v", err)
	}

	if _, err := runPatch(t, dir, `*** Begin Patch
*** Update File: sub/dir/new.txt
-line one
+Line One
*** Move to: renamed.txt
*** End Patch`); err != nil {
		t.Fatalf("move failed: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "renamed.txt")); statErr != nil {
		t.Fatalf("expected renamed.txt to exist: %v", statErr)
	}
	if got := readTestFile(t, dir, "renamed.txt"); got != "Line One\nline two\n" {
		t.Errorf("moved content: got %q", got)
	}
	if _, err := runPatch(t, dir, `*** Begin Patch
*** Delete File: renamed.txt
*** End Patch`); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "renamed.txt")); !os.IsNotExist(statErr) {
		t.Fatal("expected file to be deleted")
	}
}

func TestPatchHeredocWrapper(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "f.txt", "top\nv1\n")
	if _, err := runPatch(t, dir, "<<'EOF'\n*** Begin Patch\n*** Update File: f.txt\n@@ top\n-v1\n+V2\n*** End Patch\nEOF"); err != nil {
		t.Fatalf("patch with heredoc wrapper failed: %v", err)
	}
	if got := readTestFile(t, dir, "f.txt"); got != "top\nV2\n" {
		t.Errorf("got %q, want \"top\\nV2\\n\"", got)
	}
}

func TestPatchMultipleHunksSameFileInOrder(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "f.txt", "h\nold1\nmid\nold2\ntail\n")

	if _, err := runPatch(t, dir, `*** Begin Patch
*** Update File: f.txt
@@ h
-old1
+NEW1
@@
-old2
+NEW2
*** End Patch`); err != nil {
		t.Fatalf("patch failed: %v", err)
	}
	if got := readTestFile(t, dir, "f.txt"); got != "h\nNEW1\nmid\nNEW2\ntail\n" {
		t.Errorf("got %q, want \"h\\nNEW1\\nmid\\nNEW2\\ntail\\n\"", got)
	}
}

func TestPatchInvalidMarkerFails(t *testing.T) {
	dir := t.TempDir()
	if _, err := runPatch(t, dir, `*** Begin Patch
*** Rename File: f.txt
*** End Patch`); err == nil || !strings.Contains(err.Error(), "unexpected marker") {
		t.Fatalf("expected invalid marker error, got %v", err)
	}
}

func TestPatchPureMoveWithoutChunks(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "f.txt", "one\ntwo\n")

	if _, err := runPatch(t, dir, `*** Begin Patch
*** Update File: f.txt
*** Move to: g.txt
*** End Patch`); err != nil {
		t.Fatalf("pure move failed: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "f.txt")); !os.IsNotExist(statErr) {
		t.Fatal("source f.txt should be gone")
	}
	if got := readTestFile(t, dir, "g.txt"); got != "one\ntwo\n" {
		t.Errorf("moved content: got %q", got)
	}

	if _, err := runPatch(t, dir, `*** Begin Patch
*** Update File: g.txt
*** End Patch`); err == nil || !strings.Contains(err.Error(), "has no changes") {
		t.Fatalf("update without chunks or move must fail, got %v", err)
	}
}

func TestPatchMismatchErrorShowsFileContext(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "f.txt", "alpha\nbeta\ngamma\n")

	_, err := runPatch(t, dir, `*** Begin Patch
*** Update File: f.txt
@@ alpha
-NOPE
+NEW
*** End Patch`)
	if err == nil {
		t.Fatal("expected mismatch error")
	}
	msg := err.Error()
	for _, want := range []string{"failed to find expected lines", "NOPE", "chunk 1", "beta"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should contain %q; got:\n%s", want, msg)
		}
	}
}

func TestPatchStandaloneMoveToRejected(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "f.txt", "x\n")
	if _, err := runPatch(t, dir, `*** Begin Patch
*** Move to: g.txt
*** End Patch`); err == nil || !strings.Contains(err.Error(), "not a standalone marker") {
		t.Fatalf("expected 'not a standalone marker' error, got %v", err)
	}
}
