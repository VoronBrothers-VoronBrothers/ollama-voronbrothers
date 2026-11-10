package envconfig

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain isolates the whole package from real system env files so that
// legacy tests keep seeing a clean environment. The file-fallback behaviour
// itself is covered by TestVarFileFallback with fake directories.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "envconfig-test")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(tmp)
	setEnvFileDirsForTest(filepath.Join(tmp, "etc")) // empty dir: no system file
	os.Exit(m.Run())
}

// Fallback chain: process-env > ~/.ollama/env > /etc/ollama/env.
func TestVarFileFallback(t *testing.T) {
	tmp, err := os.MkdirTemp("", "envconfig-var-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)

	sysDir := filepath.Join(tmp, "etc", "ollama")
	userHome := t.TempDir()
	for _, dir := range []string{sysDir, filepath.Join(userHome, ".ollama")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	const sys = "from-system-file"
	const user = "from-user-file"
	writeEnvFile(t, filepath.Join(sysDir, "env"), map[string]string{
		"VORON_TEST_FILE_ENV": sys,
		"VORON_TEST_SYS_ONLY": sys,
	})
	writeEnvFile(t, filepath.Join(userHome, ".ollama", "env"), map[string]string{
		"VORON_TEST_FILE_ENV": user, // overrides system value
	})

	t.Setenv("HOME", userHome)
	setEnvFileDirsForTest(sysDir)

	if got := Var("VORON_TEST_FILE_ENV"); got != user {
		t.Errorf(`user-file override: got %q, want %q`, got, user)
	}

	// Process env wins over both files.
	os.Setenv("VORON_TEST_FILE_ENV", "from-process")
	defer os.Unsetenv("VORON_TEST_FILE_ENV")
	if got := Var("VORON_TEST_FILE_ENV"); got != "from-process" {
		t.Errorf(`process-env priority: got %q, want "from-process"`, got)
	}

	// Unknown key stays empty.
	if got := Var("VORON_TEST_NO_SUCH_KEY"); got != "" {
		t.Errorf(`unknown key = %q, want ""`, got)
	}
}

func writeEnvFile(t *testing.T, path string, kv map[string]string) {
	t.Helper()
	lines := "# comment line\n"
	for k, v := range kv {
		lines += k + "=" + v + "\n"
	}
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
}
