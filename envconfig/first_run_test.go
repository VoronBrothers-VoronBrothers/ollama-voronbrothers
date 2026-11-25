package envconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The template carries no built-in default model: BEE_AGENT_MODEL / LAMA_MODEL
// are user-provided and required. These guards stop a concrete model name or an
// old placeholder token from leaking back into the written env file.
const (
	envModelPlaceholder = "__DEFAULT_MODEL__"
	legacyDefaultModel  = "deepseek-v4.1-flash-1017"
)

func assertNoBakedInDefault(t *testing.T, s string) {
	t.Helper()
	if strings.Contains(s, envModelPlaceholder) {
		t.Errorf("file contains literal placeholder %q", envModelPlaceholder)
	}
	if strings.Contains(s, legacyDefaultModel) {
		t.Errorf("file contains a baked-in default model name %q", legacyDefaultModel)
	}
}

// The rendered template must not leak placeholders or concrete model names.
func TestRenderedEnvTemplate_NoBakedInDefault(t *testing.T) {
	assertNoBakedInDefault(t, renderedEnvTemplate())
}

// Service→Recreate path: ~/.ollama/env must be written without any baked-in
// default model.
func TestRecreateUserEnvFile_WritesNoBakedInDefault(t *testing.T) {
	setTestHome(t, t.TempDir())
	path, err := RecreateUserEnvFile()
	if err != nil {
		t.Fatalf("RecreateUserEnvFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	assertNoBakedInDefault(t, string(data))
}

// First-launch path (EnsureUserOllamaDir): the env file it seeds must also be
// clean of placeholders and concrete model names.
func TestEnsureUserOllamaDir_WritesNoBakedInDefault(t *testing.T) {
	setTestHome(t, t.TempDir())

	home, _ := os.UserHomeDir()
	envPath := filepath.Join(home, ".ollama", "env")

	// Only run the seed when the file is absent (writeIfMissing is a no-op on
	// an existing file).
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be absent before EnsureUserOllamaDir", envPath)
	}

	EnsureUserOllamaDir()

	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read %s: %v", envPath, err)
	}
	assertNoBakedInDefault(t, string(data))
}
