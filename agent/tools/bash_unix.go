//go:build !windows

package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func shellToolName() string {
	return "bash"
}

func shellToolDescription() string {
	return "Execute a bash command on the system. The working directory persists across calls — after an initial \"cd\" prefer relative paths and avoid re-typing the full path. Use it to inspect files, run tests, and perform development tasks."
}

func shellCommandDescription() string {
	return "The bash command to execute."
}

// aliasPromotionScript converts every defined `alias` into a same-named shell
// function so that aliases expand reliably inside the non-interactive "bash -c"
// invocation (plain aliases are not expanded there because the whole command
// string is parsed before it runs). Aliases whose names contain characters that
// are illegal in identifiers (e.g. hyphens) are skipped — use a function for those.
const aliasPromotionScript = `while IFS= read -r _ollama_al; do
  [ -z "$_ollama_al" ] && continue
  _ollama_s=${_ollama_al#alias }
  _ollama_n=${_ollama_s%%=*}
  _ollama_v=${_ollama_s#*=}
  case $_ollama_v in
    \"*\") _ollama_v=${_ollama_v#\"}; _ollama_v=${_ollama_v%\"} ;;
    \'*\') _ollama_v=${_ollama_v#\'}; _ollama_v=${_ollama_v%\'} ;;
  esac
  if [[ $_ollama_n =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then eval "$_ollama_n() { $_ollama_v; }"; fi
done <<< "$(alias)"`

// personalShellEnabled reports whether the agent's personal shell profile is
// active: it sources a private rc/aliases file and appends a human-readable,
// timestamped line per command ("[YYYY-MM-DD HH:MM] <command>") to a private
// history file under ~/.ollama/agent (see agentDir). Enabled by default; set
// OLLAMA_BASH_PERSONAL to off|no|0|false to fall back to plain bash.
func personalShellEnabled() bool {
	switch os.Getenv("OLLAMA_BASH_PERSONAL") {
	case "0", "off", "no", "false":
		return false
	default:
		return true
	}
}

// agentDir returns the per-user directory that holds the agent's private shell
// profile (rc.sh) and history log, keeping them separate from ~/.bash_history.
func agentDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "."
	}
	return filepath.Join(home, ".ollama", "agent")
}

func newBashCommand(ctx context.Context, command, cwdPath string) *exec.Cmd {
	personal := personalShellEnabled()
	dir := agentDir()

	var b strings.Builder
	if personal {
		b.WriteString("mkdir -p " + shellQuote(dir) + "\n")
		rcQ := shellQuote(filepath.Join(dir, "rc.sh"))
		b.WriteString("if [ -f " + rcQ + " ]; then . " + rcQ + "; fi\n")
		b.WriteString(aliasPromotionScript)
		b.WriteByte('\n')
	}
	b.WriteString(command)
	b.WriteByte('\n')
	b.WriteString("__ollama_status=$?\n")
	b.WriteString("pwd -P > " + shellQuote(cwdPath) + "\n")
	if personal {
		histQ := shellQuote(filepath.Join(dir, "history"))
		// Collapse the command to a single line so history stays one
		// human-readable entry per call; prefix it with the local time.
		flat := strings.Join(strings.Fields(command), " ")
		b.WriteString("printf '[%s] %s\\n' \"$(date '+%Y-%m-%d %H:%M')\" " + shellQuote(flat) + " >> " + histQ + "\n")
	}
	b.WriteString("exit $__ollama_status")

	cmd := exec.CommandContext(ctx, "bash", "-c", b.String())
	configureBashCommand(cmd)
	return cmd
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func configureBashCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func runBashCommand(cmd *exec.Cmd) error {
	return cmd.Run()
}

func killBashCommand(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	return nil
}
