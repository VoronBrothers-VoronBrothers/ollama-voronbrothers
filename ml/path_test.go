package ml

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestFindLibOllamaPath(t *testing.T) {
	root := t.TempDir()

	// Isolate from the real ~/.ollama/env and process env so tests are deterministic.
	t.Cleanup(func() { userHomeDirFunc = os.UserHomeDir })
	userHomeDirFunc = func() (string, error) { return root, nil }
	if orig := os.Getenv("OLLAMA_WHERE_IS_MY_LLAMA_CPP"); orig != "" {
		os.Unsetenv("OLLAMA_WHERE_IS_MY_LLAMA_CPP")
		t.Cleanup(func() { os.Setenv("OLLAMA_WHERE_IS_MY_LLAMA_CPP", orig) })
	}

	tests := []struct {
		name   string
		search libOllamaPathSearch
		dirs   []string
		want   string
	}{
		{
			name: "darwin release layout",
			search: libOllamaPathSearch{
				executable: filepath.Join(root, "darwin-app", "Ollama.app", "Contents", "Resources", "ollama"),
				goos:       "darwin",
				goarch:     "arm64",
			},
			dirs: []string{filepath.Join(root, "darwin-app", "Ollama.app", "Contents", "Resources")},
			want: filepath.Join(root, "darwin-app", "Ollama.app", "Contents", "Resources"),
		},
		{
			name: "darwin standard install layout",
			search: libOllamaPathSearch{
				executable: filepath.Join(root, "darwin-install", "bin", "ollama"),
				goos:       "darwin",
				goarch:     "arm64",
			},
			dirs: []string{filepath.Join(root, "darwin-install", "lib", "ollama")},
			want: filepath.Join(root, "darwin-install", "lib", "ollama"),
		},
		{
			name: "windows release layout",
			search: libOllamaPathSearch{
				executable: filepath.Join(root, "windows-release", "ollama.exe"),
				goos:       "windows",
				goarch:     "amd64",
			},
			dirs: []string{filepath.Join(root, "windows-release", "lib", "ollama")},
			want: filepath.Join(root, "windows-release", "lib", "ollama"),
		},
		{
			name: "windows standard install layout",
			search: libOllamaPathSearch{
				executable: filepath.Join(root, "windows-install", "bin", "ollama.exe"),
				goos:       "windows",
				goarch:     "amd64",
			},
			dirs: []string{filepath.Join(root, "windows-install", "lib", "ollama")},
			want: filepath.Join(root, "windows-install", "lib", "ollama"),
		},
		{
			name: "linux standard install layout",
			search: libOllamaPathSearch{
				executable: filepath.Join(root, "linux-install", "bin", "ollama"),
				goos:       "linux",
				goarch:     "amd64",
			},
			dirs: []string{filepath.Join(root, "linux-install", "lib", "ollama")},
			want: filepath.Join(root, "linux-install", "lib", "ollama"),
		},
		{
			name: "local linux underscore dist layout",
			search: libOllamaPathSearch{
				executable: filepath.Join(root, "linux-dev", "ollama"),
				workingDir: filepath.Join(root, "linux-dev"),
				goos:       "linux",
				goarch:     "amd64",
			},
			dirs: []string{filepath.Join(root, "linux-dev", "dist", "linux_amd64", "lib", "ollama")},
			want: filepath.Join(root, "linux-dev", "dist", "linux_amd64", "lib", "ollama"),
		},
		{
			name: "mlx-only standard install layout",
			search: libOllamaPathSearch{
				executable: filepath.Join(root, "mlx-install", "bin", "ollama"),
				goos:       "linux",
				goarch:     "amd64",
			},
			dirs: []string{filepath.Join(root, "mlx-install", "lib", "ollama")},
			want: filepath.Join(root, "mlx-install", "lib", "ollama"),
		},
		{
			name: "darwin local build layout before executable directory fallback",
			search: libOllamaPathSearch{
				executable: filepath.Join(root, "darwin-dev", "ollama"),
				workingDir: filepath.Join(root, "darwin-dev"),
				goos:       "darwin",
				goarch:     "arm64",
			},
			dirs: []string{filepath.Join(root, "darwin-dev", "build", "lib", "ollama")},
			want: filepath.Join(root, "darwin-dev", "build", "lib", "ollama"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, dir := range tt.dirs {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}

			got := findLibOllamaPath(tt.search)
			if got != tt.want {
				t.Fatalf("findLibOllamaPath() = %q, want %q; candidates: %v", got, tt.want, libOllamaPathCandidates(tt.search))
			}
		})
	}
}

func TestFindLibOllamaPathOverride(t *testing.T) {
	root := t.TempDir()

	// Isolate from the real ~/.ollama/env so tests are deterministic.
	t.Cleanup(func() { userHomeDirFunc = os.UserHomeDir })
	userHomeDirFunc = func() (string, error) { return root, nil }

	t.Run("explicit absolute override wins", func(t *testing.T) {
		override := filepath.Join(root, "custom-llama")
		if err := os.MkdirAll(override, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("OLLAMA_WHERE_IS_MY_LLAMA_CPP", override)

		search := libOllamaPathSearch{executable: filepath.Join(root, "x", "ollama"), goos: "linux", goarch: "amd64"}
		if got := findLibOllamaPath(search); got != override {
			t.Fatalf("findLibOllamaPath() = %q, want override %q; candidates: %v", got, override, libOllamaPathCandidates(search))
		}
	})

	t.Run("relative path is ignored and auto-detection applies", func(t *testing.T) {
		dir := filepath.Join(root, "rel-install", "lib", "ollama")
		exe := filepath.Join(root, "rel-install", "bin", "ollama")
		for _, d := range []string{dir, filepath.Dir(exe)} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		search := libOllamaPathSearch{executable: exe, goos: "linux", goarch: "amd64"}

		t.Setenv("OLLAMA_WHERE_IS_MY_LLAMA_CPP", "relative/lib/ollama")
		if got := findLibOllamaPath(search); got != dir {
			t.Fatalf("findLibOllamaPath() = %q, want auto-detected %q; candidates: %v", got, dir, libOllamaPathCandidates(search))
		}
	})
}

func TestFindLibOllamaPathUserFileOverride(t *testing.T) {
	root := t.TempDir()

	t.Cleanup(func() { userHomeDirFunc = os.UserHomeDir })
	userHomeDirFunc = func() (string, error) { return root, nil }
	if orig := os.Getenv("OLLAMA_WHERE_IS_MY_LLAMA_CPP"); orig != "" {
		os.Unsetenv("OLLAMA_WHERE_IS_MY_LLAMA_CPP")
		t.Cleanup(func() { os.Setenv("OLLAMA_WHERE_IS_MY_LLAMA_CPP", orig) })
	}

	// Create a fake ~/.ollama/env with a user override.
	userEnvDir := filepath.Join(root, ".ollama")
	if err := os.MkdirAll(userEnvDir, 0o755); err != nil {
		t.Fatal(err)
	}

	userPath := filepath.Join(root, "llama-b12633")
	if err := os.MkdirAll(userPath, 0o755); err != nil {
		t.Fatal(err)
	}
	envContent := fmt.Sprintf("OLLAMA_WHERE_IS_MY_LLAMA_CPP=%s\n", userPath)
	if err := os.WriteFile(filepath.Join(userEnvDir, "env"), []byte(envContent), 0o644); err != nil {
		t.Fatal(err)
	}

	search := libOllamaPathSearch{executable: filepath.Join(root, "x", "ollama"), goos: "linux", goarch: "amd64"}

	t.Run("user file wins over process env (systemd scenario)", func(t *testing.T) {
		// Simulate systemd putting old value into process env.
		oldPath := filepath.Join(root, "llama-b12608")
		if err := os.MkdirAll(oldPath, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("OLLAMA_WHERE_IS_MY_LLAMA_CPP", oldPath)

		got := findLibOllamaPath(search)
		if got != userPath {
			t.Fatalf("findLibOllamaPath() = %q, want user-file override %q; candidates: %v",
				got, userPath, libOllamaPathCandidates(search))
		}
	})

	t.Run("user file used when process env is empty", func(t *testing.T) {
		// No process env set — user file should still provide the path.
		os.Unsetenv("OLLAMA_WHERE_IS_MY_LLAMA_CPP")

		got := findLibOllamaPath(search)
		if got != userPath {
			t.Fatalf("findLibOllamaPath() = %q, want user-file value %q; candidates: %v",
				got, userPath, libOllamaPathCandidates(search))
		}
	})

	t.Run("no user file falls back to process env", func(t *testing.T) {
		// Remove the fake user env file.
		if err := os.Remove(filepath.Join(userEnvDir, "env")); err != nil {
			t.Fatal(err)
		}

		exePath := filepath.Join(root, "install", "bin", "ollama")
		libDir := filepath.Join(root, "install", "lib", "ollama")
		for _, d := range []string{filepath.Dir(exePath), libDir} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		srch := libOllamaPathSearch{executable: exePath, goos: "linux", goarch: "amd64"}

		// With no user file and no process env, auto-detection should find the lib dir.
		got := findLibOllamaPath(srch)
		if got != libDir {
			t.Fatalf("findLibOllamaPath() = %q, want auto-detected %q; candidates: %v",
				got, libDir, libOllamaPathCandidates(srch))
		}
	})
}
