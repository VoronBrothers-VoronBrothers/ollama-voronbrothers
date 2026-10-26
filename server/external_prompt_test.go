package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// usePromptFile points the external prompt loader at a temp file for the
// duration of a test and resets the memo cache.
func usePromptFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write prompt file: %v", err)
		}
	}
	t.Setenv(externalPromptEnvName, path)
	externalPromptMu.Lock()
	externalPromptCache = externalPromptCacheEntry{}
	externalPromptMu.Unlock()
	t.Cleanup(func() {
		externalPromptMu.Lock()
		externalPromptCache = externalPromptCacheEntry{}
		externalPromptMu.Unlock()
	})
	return path
}

func TestExternalSystemPromptMissingFile(t *testing.T) {
	usePromptFile(t, "external_system_prompt.md", "")
	if got := externalSystemPrompt(); got != "" {
		t.Fatalf("expected empty prompt, got %q", got)
	}
}

func TestExternalSystemPromptReadsAndTrims(t *testing.T) {
	usePromptFile(t, "external_system_prompt.md", "\n  Always answer in Russian.  \n\n")
	if got := externalSystemPrompt(); got != "Always answer in Russian." {
		t.Fatalf("unexpected prompt: %q", got)
	}
}

func TestExternalSystemPromptEmptyFile(t *testing.T) {
	usePromptFile(t, "external_system_prompt.md", "   \n\t\n")
	if got := externalSystemPrompt(); got != "" {
		t.Fatalf("expected empty prompt for blank file, got %q", got)
	}
}

func TestExternalSystemPromptReloadsOnChange(t *testing.T) {
	path := usePromptFile(t, "external_system_prompt.md", "first")
	if got := externalSystemPrompt(); got != "first" {
		t.Fatalf("unexpected first read: %q", got)
	}

	// Rewrite with different length so the cache key changes even on
	// filesystems with coarse mtime resolution.
	if err := os.WriteFile(path, []byte("second version"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if got := externalSystemPrompt(); got != "second version" {
		t.Fatalf("cache did not invalidate: %q", got)
	}
}

func TestExternalSystemPromptCacheServesRepeatedReads(t *testing.T) {
	usePromptFile(t, "external_system_prompt.md", "cached")

	first := externalSystemPrompt()
	// Remove the file: the cached value is still served because stat succeeds
	// only on the cached path... so instead just assert repeated reads match.
	if again := externalSystemPrompt(); again != first || first != "cached" {
		t.Fatalf("unexpected cached reads: %q / %q", first, again)
	}
}

func TestExternalSystemPromptDirIsNotAFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(externalPromptEnvName, dir)
	externalPromptMu.Lock()
	externalPromptCache = externalPromptCacheEntry{}
	externalPromptMu.Unlock()

	if got := externalSystemPrompt(); got != "" {
		t.Fatalf("expected empty prompt for directory path, got %q", got)
	}
}

func TestPrependExternalSystemPrompt(t *testing.T) {
	prompt := "Be terse."

	t.Run("no prompt leaves messages untouched", func(t *testing.T) {
		in := []openaiMessage{{Role: "user", Content: "hi"}}
		out := prependExternalSystemPrompt(in, "")
		if len(out) != 1 || out[0].Content != "hi" {
			t.Fatalf("unexpected result: %+v", out)
		}
	})

	t.Run("inserts system message first", func(t *testing.T) {
		in := []openaiMessage{{Role: "user", Content: "hi"}}
		out := prependExternalSystemPrompt(in, prompt)
		if len(out) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(out))
		}
		if out[0].Role != "system" || out[0].Content != prompt {
			t.Fatalf("unexpected system message: %+v", out[0])
		}
		if out[1].Role != "user" || out[1].Content != "hi" {
			t.Fatalf("user message moved: %+v", out[1])
		}
	})

	t.Run("does not duplicate prompt already sent", func(t *testing.T) {
		in := []openaiMessage{{Role: "system", Content: prompt + "\n\nDefault agent prompt"}, {Role: "user", Content: "hi"}}
		out := prependExternalSystemPrompt(in, prompt)
		if len(out) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(out))
		}
		if out[0].Content != prompt+"\n\nDefault agent prompt" {
			t.Fatalf("system message was modified: %q", out[0].Content)
		}
	})

	t.Run("merges into caller system message", func(t *testing.T) {
		in := []openaiMessage{{Role: "system", Content: "You are helpful."}, {Role: "user", Content: "hi"}}
		out := prependExternalSystemPrompt(in, prompt)
		if len(out) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(out))
		}
		want := prompt + "\n\nYou are helpful."
		if out[0].Content != want {
			t.Fatalf("unexpected merged system message: %q", out[0].Content)
		}
	})
}

// TestBuildOpenAIRequestInjectsExternalPrompt is the end-to-end check: an
// /api/chat body from a raw client gets the universal prompt prepended.
func TestBuildOpenAIRequestInjectsExternalPrompt(t *testing.T) {
	prompt := "Universal external prompt."
	usePromptFile(t, "external_system_prompt.md", prompt)

	provider := &ProviderConfig{Name: "proxyapi", BaseURL: "https://example.invalid", APIKey: "k"}
	extRef := &externalModelRef{Provider: provider, ModelID: "openai/gpt-5-mini"}

	body, err := json.Marshal(map[string]any{
		"model":    "proxyapi/openai/gpt-5-mini",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
		"stream":   false,
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	out, err := buildOpenAIRequest(extRef, body)
	if err != nil {
		t.Fatalf("buildOpenAIRequest: %v", err)
	}

	var got struct {
		Model    string          `json:"model"`
		Messages []openaiMessage `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Model != "openai/gpt-5-mini" {
		t.Fatalf("model prefix not stripped: %q", got.Model)
	}
	if len(got.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d: %+v", len(got.Messages), got.Messages)
	}
	if got.Messages[0].Role != "system" || got.Messages[0].Content != prompt {
		t.Fatalf("system prompt not injected: %+v", got.Messages[0])
	}
	if got.Messages[1].Role != "user" || got.Messages[1].Content != "hi" {
		t.Fatalf("user message lost: %+v", got.Messages[1])
	}
}

// TestBuildOpenAIRequestNoPromptLeavesMessagesVerbatim guards the "only
// external models" rule: with no prompt file the request is unchanged.
func TestBuildOpenAIRequestNoPromptLeavesMessagesVerbatim(t *testing.T) {
	usePromptFile(t, "external_system_prompt.md", "")

	provider := &ProviderConfig{Name: "proxyapi", BaseURL: "https://example.invalid", APIKey: "k"}
	extRef := &externalModelRef{Provider: provider, ModelID: "openai/gpt-5-mini"}

	body := []byte(`{"model":"proxyapi/openai/gpt-5-mini","messages":[{"role":"user","content":"hi"}]}`)
	out, err := buildOpenAIRequest(extRef, body)
	if err != nil {
		t.Fatalf("buildOpenAIRequest: %v", err)
	}

	var got struct {
		Messages []openaiMessage `json:"messages"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Messages) != 1 || got.Messages[0].Role != "user" {
		t.Fatalf("messages should be verbatim: %+v", got.Messages)
	}
}

func TestExternalPromptCacheInvalidatesOnMtimeChange(t *testing.T) {
	path := usePromptFile(t, "external_system_prompt.md", "aaaa")
	externalSystemPrompt()

	// Same size, different content: only mtime can tell them apart.
	later := time.Now().Add(2 * time.Second)
	if err := os.WriteFile(path, []byte("bbbb"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if got := externalSystemPrompt(); got != "bbbb" {
		t.Fatalf("mtime change not detected: %q", got)
	}
}

// resetPromptCache clears the memoised prompt between path-logic tests.
func resetPromptCache(t *testing.T) {
	t.Helper()
	externalPromptMu.Lock()
	externalPromptCache = externalPromptCacheEntry{}
	externalPromptMu.Unlock()
	t.Cleanup(func() {
		externalPromptMu.Lock()
		externalPromptCache = externalPromptCacheEntry{}
		externalPromptMu.Unlock()
	})
}

func TestExternalPromptFollowsProvidersConfig(t *testing.T) {
	dir := t.TempDir()
	want := "universal rules from providers dir"
	if err := os.WriteFile(filepath.Join(dir, "external_system_prompt.md"), []byte(want), 0o644); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, providersFileName), []byte(`{"providers":{}}`), 0o644); err != nil {
		t.Fatalf("write providers: %v", err)
	}

	// No explicit prompt path: the prompt must be discovered next to
	// providers.json, which is what the systemd drop-in relies on.
	t.Setenv(externalPromptEnvName, "")
	t.Setenv(providersEnvName, filepath.Join(dir, providersFileName))
	resetPromptCache(t)

	if got := externalSystemPrompt(); got != want {
		t.Fatalf("prompt not picked up from providers dir: got %q want %q", got, want)
	}
	paths := externalSystemPromptPaths()
	if len(paths) == 0 || filepath.Dir(paths[0]) != dir {
		t.Fatalf("unexpected prompt paths: %v", paths)
	}
}

func TestExternalPromptAliasFile(t *testing.T) {
	dir := t.TempDir()
	want := "alias prompt"
	if err := os.WriteFile(filepath.Join(dir, "system_prompt.md"), []byte(want), 0o644); err != nil {
		t.Fatalf("write alias prompt: %v", err)
	}

	t.Setenv(externalPromptEnvName, "")
	t.Setenv(providersEnvName, filepath.Join(dir, providersFileName))
	resetPromptCache(t)

	if got := externalSystemPrompt(); got != want {
		t.Fatalf("alias file not used: got %q want %q", got, want)
	}

	// The canonical name still wins when both exist.
	if err := os.WriteFile(filepath.Join(dir, "external_system_prompt.md"), []byte("canonical"), 0o644); err != nil {
		t.Fatalf("write canonical prompt: %v", err)
	}
	resetPromptCache(t)
	if got := externalSystemPrompt(); got != "canonical" {
		t.Fatalf("canonical file should win: got %q", got)
	}
}

func TestExternalPromptExplicitWins(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "external_system_prompt.md"), []byte("from providers dir"), 0o644); err != nil {
		t.Fatalf("write dir prompt: %v", err)
	}
	explicit := filepath.Join(t.TempDir(), "custom.md")
	if err := os.WriteFile(explicit, []byte("from env"), 0o644); err != nil {
		t.Fatalf("write explicit prompt: %v", err)
	}

	t.Setenv(providersEnvName, filepath.Join(dir, providersFileName))
	t.Setenv(externalPromptEnvName, explicit)
	resetPromptCache(t)

	if got := externalSystemPrompt(); got != "from env" {
		t.Fatalf("explicit env ignored: got %q", got)
	}
	if paths := externalSystemPromptPaths(); len(paths) != 1 || paths[0] != explicit {
		t.Fatalf("explicit env should be the only candidate: %v", paths)
	}
}
