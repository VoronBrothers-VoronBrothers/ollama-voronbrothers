package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestProviderContextWindowFor covers the resolution order used by /api/show:
// per-model override, case-insensitive per-model match, provider default.
func TestProviderContextWindowFor(t *testing.T) {
	provider := &ProviderConfig{
		Name:          "proxyapi",
		BaseURL:       "https://example.invalid",
		ContextWindow: 131072,
		ContextWindows: map[string]int{
			"qwen/qwen3.8-27b":                  262144,
			"deepseek/deepseek-v4.1-flash-1017": 0, // pruned at load time
			"google/gemini-3.5-flash":           1048576,
		},
	}

	cases := []struct {
		model string
		want  int
	}{
		{"qwen/qwen3.8-27b", 262144},
		{"QWEN/Qwen3.8-27B", 262144},                  // case-insensitive fallback
		{"google/gemini-3.5-flash", 1048576},          // exact per-model
		{"deepseek/deepseek-v4.1-flash-1017", 131072}, // zero entry -> provider default
		{"unknown/model", 131072},                     // provider default
	}
	for _, tc := range cases {
		if got := provider.ContextWindowFor(tc.model); got != tc.want {
			t.Errorf("ContextWindowFor(%q) = %d, want %d", tc.model, got, tc.want)
		}
	}

	if got := (*ProviderConfig)(nil).ContextWindowFor("a/b"); got != 0 {
		t.Errorf("nil provider should yield 0, got %d", got)
	}
	if got := (&ProviderConfig{}).ContextWindowFor("a/b"); got != 0 {
		t.Errorf("unconfigured provider should yield 0, got %d", got)
	}
}

// TestLoadProvidersContextWindow parses the documented JSON shapes and drops
// non-positive per-model entries.
func TestLoadProvidersContextWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, providersFileName)
	config := `{
	  "providers": [
	    {
	      "name": "proxyapi",
	      "base_url": "https://api.example.invalid/v1/",
	      "api_key": " k ",
	      "models": ["qwen/qwen3.8-27b", "deepseek/deepseek-v4.1-flash-1017"],
	      "context_window": 131072,
	      "context_windows": {"qwen/qwen3.8-27b": 262144, "deepseek/deepseek-v4.1-flash-1017": -5}
	    }
	  ]
	}`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv(providersEnvName, path)
	providersMu.Lock()
	loadedProviders = nil
	providersLoaded = false
	providersMu.Unlock()
	t.Cleanup(func() {
		providersMu.Lock()
		loadedProviders = nil
		providersLoaded = false
		providersMu.Unlock()
	})

	loadProviders()

	ext, ok := tryParseExternalModel("proxyapi/qwen/qwen3.8-27b")
	if !ok {
		t.Fatal("external model not recognised after load")
	}
	if ext.Provider.BaseURL != "https://api.example.invalid/v1" {
		t.Errorf("base_url not normalised: %q", ext.Provider.BaseURL)
	}
	if ext.Provider.APIKey != "k" {
		t.Errorf("api_key not trimmed: %q", ext.Provider.APIKey)
	}
	if got := ext.Provider.ContextWindowFor(ext.ModelID); got != 262144 {
		t.Errorf("per-model context window = %d, want 262144", got)
	}
	if got := ext.Provider.ContextWindowFor("deepseek/deepseek-v4.1-flash-1017"); got != 131072 {
		t.Errorf("negative per-model entry should fall back to provider default, got %d", got)
	}
	if _, exists := ext.Provider.ContextWindows["deepseek/deepseek-v4.1-flash-1017"]; exists {
		t.Error("non-positive per-model entry was kept")
	}
}

// TestShowExternalModelReportsContextLength pins the /api/show contract the
// TUI relies on to size its compaction budget.
func TestShowExternalModelReportsContextLength(t *testing.T) {
	providersMu.Lock()
	loadedProviders = []*ProviderConfig{{
		Name:           "proxyapi",
		BaseURL:        "https://api.example.invalid/v1",
		Models:         []string{"qwen/qwen3.8-27b"},
		ContextWindow:  131072,
		ContextWindows: map[string]int{"qwen/qwen3.8-27b": 262144},
	}}
	providersLoaded = true
	providersMu.Unlock()
	t.Cleanup(func() {
		providersMu.Lock()
		loadedProviders = nil
		providersLoaded = false
		providersMu.Unlock()
	})

	ext, ok := tryParseExternalModel("proxyapi/qwen/qwen3.8-27b")
	if !ok {
		t.Fatal("external model not recognised")
	}

	// Mirror the handler's Details payload and verify it round-trips as
	// "context_length" so showResponseContextWindow() picks it up.
	details := map[string]any{"family": "external", "context_length": ext.Provider.ContextWindowFor(ext.ModelID)}
	blob, err := json.Marshal(map[string]any{"details": details})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back struct {
		Details struct {
			ContextLength int `json:"context_length"`
		} `json:"details"`
	}
	if err := json.Unmarshal(blob, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Details.ContextLength != 262144 {
		t.Fatalf("context_length = %d, want 262144", back.Details.ContextLength)
	}
}
