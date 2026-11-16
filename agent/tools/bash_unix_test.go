//go:build !windows

package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ollama/ollama/agent"
)

func TestConfigureBashCommandSetsProcessGroup(t *testing.T) {
	cmd := exec.Command("bash", "-c", "true")
	configureBashCommand(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Fatalf("configureBashCommand should start bash in a new process group")
	}
}

func TestBashWaitDelayBoundsBackgroundOutputPipe(t *testing.T) {
	start := time.Now()
	result, err := (&Bash{}).Execute(context.Background(), agent.ToolContext{WorkingDir: t.TempDir()}, map[string]any{
		"command": "sleep 5 & echo done",
	})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > bashWaitDelay+2*time.Second {
		t.Fatalf("command elapsed = %s, want bounded near %s", elapsed, bashWaitDelay)
	}
	if !strings.Contains(result.Content, "done") {
		t.Fatalf("content = %q, want command output", result.Content)
	}
	if !strings.Contains(result.Content, "output pipes did not close") {
		t.Fatalf("content = %q, want wait delay message", result.Content)
	}
}

// setTestHome points HOME (and thus agentDir()) at a fresh dir and restores it.
func setTestHome(t *testing.T) string {
	t.Helper()
	old := os.Getenv("HOME")
	t.Cleanup(func() { _ = os.Setenv("HOME", old) })
	home := t.TempDir()
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestBashPersonalProfileAliasesAndHistory(t *testing.T) {
	home := setTestHome(t)
	dir := filepath.Join(home, ".ollama", "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rc := "alias sayhi='echo HELLO_FROM_RC'\n" +
		"export OLLAMA_TEST_ENV=abc\n"
	if err := os.WriteFile(filepath.Join(dir, "rc.sh"), []byte(rc), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := (&Bash{}).Execute(context.Background(), agent.ToolContext{WorkingDir: home}, map[string]any{
		"command": "sayhi && echo env=$OLLAMA_TEST_ENV",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(result.Content, "HELLO_FROM_RC") {
		t.Fatalf("alias not expanded; content = %q", result.Content)
	}
	if !strings.Contains(result.Content, "env=abc") {
		t.Fatalf("rc env not sourced; content = %q", result.Content)
	}

	hist := filepath.Join(dir, "history")
	data, err := os.ReadFile(hist)
	if err != nil {
		t.Fatalf("history file not created: %v", err)
	}
	if !strings.Contains(string(data), "sayhi && echo env=$OLLAMA_TEST_ENV") {
		t.Fatalf("command not recorded in history; got = %q", string(data))
	}
}

func TestBashPersonalProfileDisabledByEnv(t *testing.T) {
	home := setTestHome(t)
	dir := filepath.Join(home, ".ollama", "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rc.sh"), []byte("alias sayhi='echo HELLO_FROM_RC'\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	old := os.Getenv("OLLAMA_BASH_PERSONAL")
	t.Cleanup(func() { _ = os.Setenv("OLLAMA_BASH_PERSONAL", old) })
	if err := os.Setenv("OLLAMA_BASH_PERSONAL", "off"); err != nil {
		t.Fatal(err)
	}

	result, err := (&Bash{}).Execute(context.Background(), agent.ToolContext{WorkingDir: home}, map[string]any{
		"command": "sayhi",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Contains(result.Content, "HELLO_FROM_RC") {
		t.Fatalf("personal profile should be disabled; content = %q", result.Content)
	}
	if _, err := os.Stat(filepath.Join(dir, "history")); !os.IsNotExist(err) {
		t.Fatalf("history file should not exist when personal shell is off")
	}
}
