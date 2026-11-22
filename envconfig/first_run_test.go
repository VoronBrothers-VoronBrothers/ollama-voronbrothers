package envconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The raw template keeps a __DEFAULT_MODEL__ placeholder; rendering resolves it
// to the single source of truth (DefaultModel). These guards stop either side
// from drifting: hardcoding a model back into the template, or leaving the
// literal token in a written file.
const envModelPlaceholder = "__DEFAULT_MODEL__"

func TestRenderedEnvTemplate_ResolvesDefaultModel(t *testing.T) {
	got := renderedEnvTemplate()
	if strings.Contains(got, envModelPlaceholder) {
		t.Errorf("rendered template still contains placeholder %q", envModelPlaceholder)
	}
	if !strings.Contains(got, DefaultModel) {
		t.Errorf("rendered template missing DefaultModel %q", DefaultModel)
	}
}

// Guards that the render is meaningful: the raw template must carry the token.
func TestRawTemplateHasDefaultModelPlaceholder(t *testing.T) {
	if !strings.Contains(envFileTemplate, envModelPlaceholder) {
		t.Errorf("envFileTemplate lost %q — rendering has nothing to resolve", envModelPlaceholder)
	}
}

// Service→Recreate path: ~/.ollama/env must contain the resolved model, never
// the literal token.
func TestRecreateUserEnvFile_WritesResolvedDefault(t *testing.T) {
	setTestHome(t, t.TempDir())
	path, err := RecreateUserEnvFile()
	if err != nil {
		t.Fatalf("RecreateUserEnvFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	assertRendered(t, data)
}

// First-launch path (EnsureUserOllamaDir): the env file it seeds must also be
// resolved. This is the exact spot where a raw template write used to leak
// __DEFAULT_MODEL__ into ~/.ollama/env.
func TestEnsureUserOllamaDir_WritesResolvedDefault(t *testing.T) {
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
	assertRendered(t, data)
}

func assertRendered(t *testing.T, data []byte) {
	t.Helper()
	s := string(data)
	if strings.Contains(s, envModelPlaceholder) {
		t.Errorf("written env file contains literal %q — template not rendered", envModelPlaceholder)
	}
	if !strings.Contains(s, DefaultModel) {
		t.Errorf("written env file missing DefaultModel %q", DefaultModel)
	}
}
