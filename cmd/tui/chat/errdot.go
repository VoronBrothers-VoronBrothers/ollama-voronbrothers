package chat

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
	"time"
)

// errDotInterval — минимальная пауза между отправками точки (анти-спам).
const errDotInterval = 30 * time.Second

var lastErrDotUnixNano atomic.Int64

func errDotSession() string {
	if s := os.Getenv("OLLAMA_ERRDOT_SESSION"); s != "" {
		return s
	}
	return "orchestrator-this-is-your-own-tmux-send-pictures-here-with-task"
}

// notifyErrorDot шлёт в tmux-сессию чата точку+Enter (перезапуск агента),
// не чаще чем раз в 30 секунд. Вызывается при каждом error-entry в TUI.
func notifyErrorDot() {
	now := time.Now().UnixNano()
	last := lastErrDotUnixNano.Load()
	if last != 0 && now-last < int64(errDotInterval) {
		return
	}
	if !lastErrDotUnixNano.CompareAndSwap(last, now) {
		return // кто-то другой успел первым — он и шлёт
	}
	go sendErrDot()
}

func sendErrDot() {
	session := errDotSession()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cmds := [][]string{
		{"tmux", "send-keys", "-t", session, "-l", "."},
		{"tmux", "send-keys", "-t", session, "Enter"},
	}
	var fails []string
	for _, a := range cmds {
		if err := exec.CommandContext(ctx, a[0], a[1:]...).Run(); err != nil {
			fails = append(fails, fmt.Sprintf("%v: %v", a, err))
		}
	}

	line := time.Now().Format("2006-01-02 15:04:05") + " dot -> tmux " + session
	if len(fails) > 0 {
		line += " ERRORS: " + fmt.Sprint(fails)
	} else {
		line += " OK"
	}
	f, err := os.OpenFile("/tmp/ollama-errdot.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err == nil {
		fmt.Fprintln(f, line)
		f.Close()
	}
}
