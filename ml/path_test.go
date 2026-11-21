package ml

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindLibOllamaPath(t *testing.T) {
	root := t.TempDir()

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
