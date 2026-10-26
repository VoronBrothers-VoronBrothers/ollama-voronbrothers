package server

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ollama/ollama/api"
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
	providersMu     sync.RWMutex
	loadedProviders []*ProviderConfig
	providersLoaded bool
)

// providersEnvName is an optional explicit path to the providers config file.
// [voron] lets the daemon (running as user "ollama") read a config that lives in
// another user's home, e.g. /home/voron/.ollama/providers.json.
const providersEnvName = "OLLAMA_PROVIDERS_CONFIG"

// providerConfigPaths returns the candidate config paths in priority order:
// explicit $OLLAMA_PROVIDERS_CONFIG, then ~/.ollama/providers.json (honouring
// $OLLAMA_HOME), then the system-wide /etc/ollama/providers.json fallback.
func providerConfigPaths() []string {
	var paths []string
	add := func(p string) {
		if p == "" {
			return
		}
		clean := filepath.Clean(p)
		for _, existing := range paths {
			if sameFile(existing, clean) {
				return
			}
		}
		paths = append(paths, clean)
	}

	// [voron] an explicit OLLAMA_PROVIDERS_CONFIG is authoritative: it is the
	// only path consulted, so a missing/typo'd path never silently falls back
	// to another user's home config. Tests use this to stay hermetic.
	if explicit := strings.TrimSpace(os.Getenv(providersEnvName)); explicit != "" {
		return []string{filepath.Clean(explicit)}
	}

	configDir := strings.TrimSpace(os.Getenv("OLLAMA_HOME"))
	if configDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			configDir = filepath.Join(home, ".ollama")
		} else {
			slog.Warn("providers: cannot determine home directory", "error", err)
		}
	}
	add(filepath.Join(configDir, providersFileName))

	// [voron] system-wide fallback: the service runs as user "ollama" with a
	// different home, so ~/.ollama/providers.json is not visible to it.
	add(filepath.Join("/etc", "ollama", providersFileName))
	return paths
}

// loadProviders reads and parses the external providers config. Called once at startup.
func loadProviders() {
	paths := providerConfigPaths()
	if len(paths) == 0 {
		slog.Warn("providers: no config paths available")
		return
	}

	var data []byte
	var err error
	configPath := ""
	for _, pth := range paths {
		configPath = pth
		data, err = os.ReadFile(pth)
		if err == nil {
			break
		}
	}
	if err != nil {
		if os.IsNotExist(err) {
			slog.Debug("providers: config file not found, no external providers", "path", configPath)
		} else {
			slog.Warn("providers: failed to read config", "path", configPath, "error", err)
		}
		return
	}

	var file struct {
		Providers []*ProviderConfig `json:"providers"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		slog.Warn("providers: failed to parse config", "path", configPath, "error", err)
		return
	}
	slog.Info("providers: config loaded", "path", configPath, "providers", len(file.Providers))

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

// externalListModelResponses returns external models as /api/tags rows so the
// TUI model picker and `ollama list` can show them (for TUI listing).
func externalListModelResponses() []api.ListModelResponse {
	providersMu.RLock()
	defer providersMu.RUnlock()

	var out []api.ListModelResponse
	seen := make(map[string]struct{})
	for _, p := range loadedProviders {
		for _, m := range p.Models {
			name := strings.TrimSpace(p.Name) + "/" + strings.TrimSpace(m)
			if name == "//" || strings.Contains(name, "  ") {
				continue
			}
			key := strings.ToLower(name)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, api.ListModelResponse{Name: name, Model: name})
		}
	}
	return out
}

func sameFile(a, b string) bool {
	aa, ea := filepath.Abs(a)
	bb, eb := filepath.Abs(b)
	if ea != nil || eb != nil {
		return false
	}
	return aa == bb
}
