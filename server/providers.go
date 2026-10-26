package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ProviderConfig describes an external API provider loaded from the config file.
type ProviderConfig struct {
	Name    string   `json:"name"`
	BaseURL string   `json:"base_url"`
	APIKey  string   `json:"api_key"`
	Models  []string `json:"models"`
}

// providersFile is the location of the external providers configuration.
const providersFileName = "providers.json"

var (
	providersMu      sync.RWMutex
	loadedProviders  []*ProviderConfig
	providersLoaded  bool
)

// loadProviders reads and parses ~/.ollama/providers.json. Called once at startup.
func loadProviders() {
	configDir := os.Getenv("OLLAMA_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			slog.Warn("providers: cannot determine home directory", "error", err)
			return
		}
		configDir = filepath.Join(home, ".ollama")
	}

	path := filepath.Join(configDir, providersFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Debug("providers: config file not found, no external providers", "path", path)
		} else {
			slog.Warn("providers: failed to read config", "path", path, "error", err)
		}
		return
	}

	var file struct {
		Providers []*ProviderConfig `json:"providers"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		slog.Warn("providers: failed to parse config", "path", path, "error", err)
		return
	}

	for _, p := range file.Providers {
		p.Name = strings.TrimSpace(p.Name)
		p.BaseURL = strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
		p.APIKey = strings.TrimSpace(p.APIKey)
		if p.Name == "" || p.BaseURL == "" || len(p.Models) == 0 {
			slog.Warn("providers: skipping invalid provider entry", "name", p.Name)
			continue
		}
		slog.Info("providers: loaded external provider", "name", p.Name, "models", len(p.Models))
	}

	providersMu.Lock()
	loadedProviders = file.Providers
	providersLoaded = true
	providersMu.Unlock()
}

// findProvider looks up a provider by name (case-insensitive). Returns nil if not found.
func findProvider(name string) *ProviderConfig {
	providersMu.RLock()
	defer providersMu.RUnlock()

	for _, p := range loadedProviders {
		if strings.EqualFold(p.Name, name) {
			return p
		}
	}
	return nil
}

// externalModelRef holds a parsed reference to an external model.
type externalModelRef struct {
	Provider *ProviderConfig // the matched provider
	ModelID  string          // the model identifier to send in API request (e.g., "openai/gpt-5-mini")
}

// tryParseExternalModel checks if raw is an external model name matching a configured provider.
// Format: "provider/vendor/model-name" — first segment must match a provider Name.
func tryParseExternalModel(raw string) (*externalModelRef, bool) {
	if raw == "" {
		return nil, false
	}

	// Split on first "/" to get potential provider name
	slashIdx := strings.Index(raw, "/")
	if slashIdx <= 0 {
		return nil, false
	}

	providerName := raw[:slashIdx]
	modelID := raw[slashIdx+1:]

	// ModelID must contain at least one more "/" (vendor/model)
	if !strings.Contains(modelID, "/") {
		return nil, false
	}

	provider := findProvider(providerName)
	if provider == nil {
		return nil, false
	}

	return &externalModelRef{
		Provider: provider,
		ModelID:  modelID,
	}, true
}

// allExternalModels returns the full list of external model names (for TUI listing).
func allExternalModels() []string {
	providersMu.RLock()
	defer providersMu.RUnlock()

	var result []string
	for _, p := range loadedProviders {
		for _, m := range p.Models {
			result = append(result, p.Name+"/"+m)
		}
	}
	return result
}

// HasExternalProviders reports whether any external provider is configured.
func hasExternalProviders() bool {
	providersMu.RLock()
	defer providersMu.RUnlock()
	return len(loadedProviders) > 0
}

var _ = fmt.Sprintf // keep import if unused in future refactor
