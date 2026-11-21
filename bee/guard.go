package main

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Защита от «самоубийственных» команд под-агента.
//
// Вторая (после старого `kill 0`) причина, по которой под-агенты внезапно
// исчезают «как будто кто-то выполнил tmux kill-server»: агент сам выполняет
// такую команду в run_command. Он живёт в tmux-сессии того же сервера, поэтому
// `tmux kill-server`, `killall tmux`, `pkill -f tmux`, `kill 0` или убийство
// собственного pid сносят и его, и все остальные сессии (включая основную).
//
// Мы не даём этому случиться: команда отклоняется с внятным объяснением, чтобы
// модель выбрала безопасный вариант (kill-session только своей сессии и т.п.).

// suicidalPatterns — шаблоны, ловящие снос tmux-сервера / всей группы / себя.
var suicidalPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\btmux\s+kill-server\b`),
	regexp.MustCompile(`\bkillall\b[^\n|;&]*\btmux`),
	regexp.MustCompile(`\bpkill\b[^\n|;&]*\btmux`),
	regexp.MustCompile(`\bkill\s+(-\w+\s+)*0\b`),  // kill [sig] 0 — вся группа
	regexp.MustCompile(`\bkill\s+(-\w+\s+)*-1\b`), // kill -1 — все процессы юзера
	regexp.MustCompile(`\bkillall\b[^\n|;&]*\bbash`),
	regexp.MustCompile(`\bkillall\b[^\n|;&]*\bbe[e]`),  // killall bee / beex (escape self-match)
	regexp.MustCompile(`\bpkill\s+(?:-\w+\s+)*[-x]?\s*bee`), // pkill -f/-x bee
}

// rejectSuicidalCommand возвращает понятную ошибку, если команда снесёт
// tmux-сервер, всю process group или сам процесс bee.
func rejectSuicidalCommand(command string) error {
	flat := strings.ToLower(command)
	for _, re := range suicidalPatterns {
		if re.MatchString(flat) {
			return fmt.Errorf("refusing to run: this command would kill the tmux server / " +
				"the agent itself (and every other session). Use a narrower target " +
				"(e.g. `tmux kill-session -t <your-session>`) or don't kill processes at all")
		}
	}

	// tmux kill-session / kill-pane: разрешаем ТОЛЬКО чужие сессии, не свою.
	if self := os.Getenv("BEE_SESSION"); self != "" {
		for _, sub := range []string{"kill-session", "kill-pane"} {
			// Ловим как литерал имени сессии, так и shell-переменную $BEE_SESSION.
			if strings.Contains(flat, sub) &&
				(strings.Contains(flat, self) ||
				strings.Contains(flat, "bee_session")) {
				return fmt.Errorf("refusing to run: `tmux %s` targets your own session — the agent would kill itself. Kill a different session or don't kill tmux objects.", sub)
			}
		}
	}

	// Явное убийство своего pid (kill <bee pid>).
	self := strconv.Itoa(os.Getpid())
	for _, pat := range []string{"kill " + self, "kill -9 " + self, "kill -15 " + self} {
		if strings.Contains(flat, pat) {
			return fmt.Errorf("refusing to run: this command kills the agent's own process (pid %s)", self)
		}
	}
	return nil
}
