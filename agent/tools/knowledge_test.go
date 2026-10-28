package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ollama/ollama/agent"
)

// withHome points HOME at a temp dir so knowledge.Dir() lands there.
func withHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".ollama", "knowledge")
}

func runKnowledgeSearch(t *testing.T, query string) string {
	t.Helper()
	res, err := (KnowledgeSearch{}).Execute(context.Background(), agent.ToolContext{}, map[string]any{"query": query})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return res.Content
}

func TestKnowledgeSearchEmptyBase(t *testing.T) {
	dir := withHome(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out := runKnowledgeSearch(t, "anything")
	if !strings.Contains(out, "empty") {
		t.Fatalf("want empty-base message, got: %s", out)
	}
}

func TestKnowledgeSearchFindsContentAndReindexes(t *testing.T) {
	dir := withHome(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeKBFile(t, filepath.Join(dir, "notes.md"), "# Ollama notes\n\nOllama runs local models through a simple HTTP API.\n")

	out := runKnowledgeSearch(t, "local model runner http api")
	if !strings.Contains(out, "notes.md") || !strings.Contains(out, "HTTP API") {
		t.Fatalf("want notes.md hit with content, got: %s", out)
	}

	// New file appears on disk: next search must transparently reindex and find it.
	writeKBFile(t, filepath.Join(dir, "weather.txt"), "The weather in Voronezh is rainy.\n")
	out = runKnowledgeSearch(t, "voronezh rain")
	if !strings.Contains(out, "weather.txt") {
		t.Fatalf("want weather.txt hit after lazy reindex, got: %s", out)
	}
}

func TestKnowledgeSearchNoMatch(t *testing.T) {
	dir := withHome(t)
	writeKBFile(t, filepath.Join(dir, "a.md"), "# one\n\ntext here\n")
	out := runKnowledgeSearch(t, "zzzqqq xxx")
	if !strings.Contains(out, "No relevant results") {
		t.Fatalf("want no-match message, got: %s", out)
	}
}

func TestKnowledgeSearchEmptyQuery(t *testing.T) {
	withHome(t)
	out := runKnowledgeSearch(t, "   ")
	if !strings.Contains(out, "empty") || !strings.Contains(strings.ToLower(out), "error") {
		t.Fatalf("want empty-query error, got: %s", out)
	}
}

func writeKBFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
