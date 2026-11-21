package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Профиль субагента: rc.sh + история команд.
// Аналог механизма основного агента (~/.ollama/agent/rc.sh + history),
// но общий для ВСЕХ субагентов. В будущем можно расширить до per-session.

// subAgentDir — корневая папка профиля всех субагентов.
func subAgentDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "/tmp"
	}
	dir := filepath.Join(home, ".ollama", "subagent")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// historyPath — путь к файлу истории команд всех субагентов.
func historyPath() string { return filepath.Join(subAgentDir(), "history") }

// rcPath — путь к общему rc.sh для всех субагентов.
func rcPath() string { return filepath.Join(subAgentDir(), "rc.sh") }

// profileInitCommands — строка для первого запуска персистентного bash:
// source'ит rc.sh и promotes алиасы в функции (через temp-файл, чтобы не ломать
// парсинг pipe-input при сложном инлайне).
func profileInitCommands() string {
	rcQ := "'" + rcPath() + "'"

	var b strings.Builder
	b.WriteString("[ -f ")
	b.WriteString(rcQ)
	b.WriteString(" ] && . ")
	b.WriteString(rcQ)
	b.WriteString(" 2>/dev/null; ")
	// Алиасы: генерим функции в temp-файл, source'им его.
	b.WriteString("_bee_p=$(mktemp); ")
	b.WriteString(aliasPromoteScript)
	b.WriteString(" > \"$_bee_p\"; [ -s \"$_bee_p\" ] && . \"$_bee_p\" 2>/dev/null; rm -f \"$_bee_p\"; true")
	return b.String()
}

// aliasPromoteScript — для каждого `alias` из rc.sh генерирует функцию в stdout.
// Простая конструкция без case/eval — надёжна при pipe-input bash.
const aliasPromoteScript = "while IFS= read -r _bee_al; do [ -z \"$_bee_al\" ] && continue; _bee_s=${_bee_al#alias }; _bee_n=${_bee_s%%=*}; _bee_v=${_bee_s#*=}; if [[ $_bee_n =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then printf '%s() { %s; }\\n' \"$_bee_n\" \"$_bee_v\"; fi; done <<< \"$(alias)\""

// appendHistory — пишет одну строку в файл истории: [YYYY-MM-DD HH:MM] <command>.
func appendHistory(command string) {
	flat := strings.Join(strings.Fields(command), " ")
	ts := time.Now().Format("2006-01-02 15:04")
	line := fmt.Sprintf("[%s] %s\n", ts, flat)

	f, err := os.OpenFile(historyPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return // best effort — история не должна ронять агента
	}
	defer f.Close()
	fmt.Fprint(f, line)
}
