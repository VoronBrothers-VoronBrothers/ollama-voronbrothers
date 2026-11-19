package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ollama/ollama/agent"
)

// Чтение state-файла под-агента (пишет bee, см. bee/state.go).
//
// Зачем отдельный файл: раньше «работает / закончил» определялось разбором
// последней строки tmux-пейна. Это ломается от любого эха, многоточия или
// длинной строки, которая переносится — и основная модель не понимает, живой
// ли агент. Теперь bee атомарно пишет JSON со статусом, номером раунда,
// текущим инструментом и heartbeat (UpdatedAt). Делегат просто читает файл.

// statusLocalIdle — строка статуса «задача выполнена» в state-файле bee.
// Должна совпадать с statusIdle в bee/state.go.
const statusLocalIdle = "idle"

// beeStateStaleAfter — сколько bee может молчать (не обновлять heartbeat),
// прежде чем мы честно скажем «завис/заморожен». bee стучит раз в 5 с.
const beeStateStaleAfter = 45 * time.Second

// beeStateSnapshot — зеркало beeState из bee/state.go (только нужные поля).
type beeStateSnapshot struct {
	PID        int       `json:"pid"`
	Model      string    `json:"model"`
	Task       string    `json:"task"`
	Status     string    `json:"status"`
	Phase      string    `json:"phase"`
	Tool       string    `json:"tool"`
	Round      int       `json:"round"`
	RoundsLeft int       `json:"rounds_left"`
	Waiting    bool      `json:"waiting"`
	Reason     string    `json:"reason"`
	StartedAt  time.Time `json:"started_at"`
	PhaseSince time.Time `json:"phase_since"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// stateFilePath — путь state-файла для сессии tmux. Один и тот же путь знают
// обе стороны: delegate (передаёт через env BEE_STATE) и bee (пишет).
func stateFilePath(session string) string {
	return filepath.Join(os.TempDir(), "bee-state", session+".json")
}

// readBeeState читает state-файл. ok=false, если файла нет или он битый.
func readBeeState(path string) (beeStateSnapshot, bool) {
	var st beeStateSnapshot
	if path == "" {
		return st, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return st, false
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, false
	}
	return st, true
}

// age — сколько прошло с последнего heartbeat.
func (s beeStateSnapshot) age() time.Duration {
	if s.UpdatedAt.IsZero() {
		return 0
	}
	return time.Since(s.UpdatedAt)
}

// humanStatus — одна строка статуса для основной модели: иконка + расшифровка.
// Всегда содержит ЯВНЫЙ глагол (работает / закончил / упал), чтобы модель не
// догадывалась по косвенным признакам.
func (s beeStateSnapshot) humanStatus() string {
	switch s.Status {
	case "working":
		line := "▶ WORKING"
		line += fmt.Sprintf(" (round %d", s.Round)
		if s.Tool != "" {
			line += ", tool " + s.Tool
		}
		line += ")"
		if s.Phase != "" {
			line += " · " + s.Phase
		}
		if ph := s.phaseAge(); ph > 0 {
			line += fmt.Sprintf(" for %s", shortDur(ph))
		}
		return line
	case "starting":
		return "… STARTING"
	case "idle":
		line := "✅ FINISHED (task done"
		if s.Round > 0 {
			line += fmt.Sprintf(", %d round(s)", s.Round)
		}
		if s.Reason != "" {
			line += ", " + s.Reason
		}
		if s.Waiting {
			line += "; process alive, waiting for a new task"
		}
		return line + ")"
	case "error":
		return "❌ FAILED: " + s.Reason
	case "max-rounds":
		return "⛔ STOPPED: " + s.Reason
	case "exited":
		return "💀 EXITED (process is gone"
	default:
		return s.Status
	}
}

// phaseAge — сколько длится текущая фаза (PhaseSince).
func (s beeStateSnapshot) phaseAge() time.Duration {
	if s.PhaseSince.IsZero() {
		return 0
	}
	return time.Since(s.PhaseSince)
}

// stalled — процесс жив (сессия есть), но heartbeat давно не обновлялся.
func (s beeStateSnapshot) stalled() bool {
	return s.age() > beeStateStaleAfter
}

func shortDur(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d / time.Minute)
	if m < 60 {
		return fmt.Sprintf("%dm %ds", m, int((d % time.Minute).Seconds()))
	}
	return fmt.Sprintf("%dh %dm", m/60, m%60)
}

// agentStatusLine собирает итоговую строку статуса из state-файла и
// состояния tmux-сессии. Приоритет: paused → state → эвристика по пейну.
//
// Важно: bee запускается через setsid и потому может пережить смерть
// tmux-сессии (осиротевший процесс продолжает работать). Поэтому при СВЕЖЕМ
// heartbeat верим файлу, а не tmux; при мёртвой сессии и молчащем heartbeat —
// это DEAD, а если файла нет вовсе — старая эвристика по пейну.
func (d *Delegate) agentStatusLine(a *agentInfo) string {
	if paused := d.pausedLine(a); paused != "" {
		return paused
	}
	alive := tmuxSessionAlive(a.session)
	st, ok := readBeeState(a.statePath)

	if ok {
		fresh := !st.stalled()
		finished := st.Status == statusLocalIdle || st.Status == "error" || st.Status == "max-rounds" || st.Status == "exited"
		switch {
		case st.Status == "exited":
			return "💀 EXITED (process is gone; " + reasonOr(st, "last state exited") + ")"
		case !alive && !fresh:
			// Сессия мертва и heartbeat затих — агент действительно убит.
			return fmt.Sprintf("💀 DEAD (tmux session gone, no heartbeat %s; last: %s)", shortDur(st.age()), reasonOr(st, st.Status))
		case !alive && finished:
			// Задача завершилась, процесс уже вышел — это нормальный финал.
			return st.humanStatus()
		case !alive:
			// Осиротевший, но живой bee: сессию убили, мозг продолжает работу.
			return st.humanStatus() + " (tmux session gone — brain survived as a detached process)"
		case !fresh:
			return fmt.Sprintf("🛑 STALLED (no heartbeat %s; last: %s)", shortDur(st.age()), reasonOr(st, st.Status))
		default:
			return st.humanStatus()
		}
	}

	// Файла нет (старый bee или падение до инициализации) — старая эвристика.
	switch {
	case !alive:
		return "💀 DEAD (tmux session gone)"
	case lastPaneLineIsDone(a.session):
		return "✅ FINISHED (pane marker)"
	default:
		return "▶ WORKING (no state file — heuristic)"
	}
}

// reasonOr — reason из state-файла, либо запасной текст.
func reasonOr(st beeStateSnapshot, fallback string) string {
	if st.Reason != "" {
		return st.Reason
	}
	return fallback
}

// pausedLine — расшифровка паузы (общая для list и status).
func (d *Delegate) pausedLine(a *agentInfo) string {
	d.mu.Lock()
	paused := a.paused
	pids := append([]int(nil), a.stoppedPids...)
	d.mu.Unlock()
	if !paused {
		return ""
	}
	frozen := 0
	for _, pid := range pids {
		if procState(pid) == "T" {
			frozen++
		}
	}
	switch {
	case frozen > 0:
		return fmt.Sprintf("⏸ PAUSED (frozen %d pid)", frozen)
	case tmuxSessionAlive(a.session):
		return "⚠️ PAUSE LOST"
	default:
		return "⏸ paused (session gone)"
	}
}

// stateDetail — вторая строка для list: раунды, фаза, свежесть.
func stateDetail(a *agentInfo) string {
	st, ok := readBeeState(a.statePath)
	if !ok {
		return ""
	}
	parts := []string{fmt.Sprintf("round %d (%d left)", st.Round, st.RoundsLeft)}
	if !st.StartedAt.IsZero() {
		parts = append(parts, "started "+st.StartedAt.Format("15:04:05"))
	}
	parts = append(parts, "heartbeat "+shortDur(st.age())+" ago")
	return "    state: " + joinNonEmpty(parts, " · ")
}

func joinNonEmpty(parts []string, sep string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += sep
		}
		out += p
	}
	return out
}

// withStateEnv оборачивает команду мозга так, чтобы bee знал, куда писать
// state-файл (BEE_STATE) и как называется его tmux-сессия (BEE_SESSION).
// Оборачиваем в bash -c с экспортом: tmux получает одну строку (см. agentCommand),
// а env-префикс внутри корректно применяется к setsid-обёртке.
func withStateEnv(cmdArgs []string, statePath, session string) []string {
	if len(cmdArgs) == 0 {
		return cmdArgs
	}
	prefix := "BEE_STATE=" + shellQuote(statePath) + " BEE_SESSION=" + shellQuote(session) + " "
	out := make([]string, len(cmdArgs))
	copy(out, cmdArgs)
	// cmdArgs имеет вид: ["/bin/bash -c '<script>'"] — env-префикс ставим перед
	// /bin/bash, чтобы он применился ко всему дереву (setsid наследует env).
	out[0] = prefix + out[0]
	return out
}

// markStateStopped — правка state-файла после ручного stop: чтобы повторный
// list не показывал «работает» для уже убитого агента.
func markStateStopped(statePath string) {
	st, ok := readBeeState(statePath)
	if !ok {
		return
	}
	st.Status = "exited"
	st.Phase = "stopped by supervisor"
	st.Reason = "stopped by supervisor"
	st.UpdatedAt = time.Now()
	if data, err := json.Marshal(st); err == nil {
		_ = os.WriteFile(statePath, data, 0o644)
	}
}

// status — подробный статус одного агента (action "status").
func (d *Delegate) status(id string) (agent.ToolResult, error) {
	a := d.getAgent(id)
	if a == nil {
		return agent.ToolResult{Content: fmt.Sprintf("Error: no such agent %q", id)}, nil
	}
	line := d.agentStatusLine(a)
	var b strings.Builder
	fmt.Fprintf(&b, "agent %s: %s\n", id, line)
	fmt.Fprintf(&b, "session: %s (pid %d), age %s\n", a.session, a.panePID, time.Since(a.spawnedAt).Round(time.Second))
	if st, ok := readBeeState(a.statePath); ok {
		fmt.Fprintf(&b, "model: %s\n", st.Model)
		fmt.Fprintf(&b, "rounds: %d used, %d left\n", st.Round, st.RoundsLeft)
		if st.Tool != "" {
			fmt.Fprintf(&b, "current tool: %s\n", st.Tool)
		}
		if st.Phase != "" {
			fmt.Fprintf(&b, "phase: %s (for %s)\n", st.Phase, shortDur(st.phaseAge()))
		}
		fmt.Fprintf(&b, "heartbeat: %s ago\n", shortDur(st.age()))
		if st.Reason != "" {
			fmt.Fprintf(&b, "reason: %s\n", st.Reason)
		}
	} else {
		b.WriteString("state file: missing (old bee build or crashed at startup)\n")
	}
	fmt.Fprintf(&b, "task: %s\n", a.task)
	b.WriteString(agentWorkNote(a, line))
	return agent.ToolResult{Content: strings.TrimSpace(b.String())}, nil
}

// agentWorkNote — короткая однозначная приписка для основной модели:
// работает / закончил / упал. Отдельной строкой, чтобы её нельзя было
// спутать с выводом самого агента.
func agentWorkNote(a *agentInfo, statusLine string) string {
	switch {
	case strings.Contains(statusLine, "PAUSED"), strings.Contains(statusLine, "STALLED"):
		return "\n⚠️ AGENT IS NOT PROGRESSING / АГЕНТ НЕ РАБОТАЕТ: " + statusLine
	case strings.Contains(statusLine, "WORKING"), strings.Contains(statusLine, "STARTING"):
		return "\n⏳ AGENT IS STILL WORKING / АГЕНТ ЕЩЁ РАБОТАЕТ: " + statusLine
	case strings.Contains(statusLine, "FINISHED"):
		return "\n✅ AGENT HAS FINISHED / АГЕНТ ЗАКОНЧИЛ: " + statusLine
	default:
		return "\n❌ AGENT IS NOT RUNNING / АГЕНТ НЕ РАБОТАЕТ: " + statusLine
	}
}
