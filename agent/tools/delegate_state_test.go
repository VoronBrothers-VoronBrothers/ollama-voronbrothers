package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeState кладёт state-файл агента (как это делает bee) во временный каталог.
func writeState(t *testing.T, session string, st beeStateSnapshot) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), session+".json")
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// TestHumanStatusWorking — работающий агент показывается явно: WORKING + раунд + инструмент.
func TestHumanStatusWorking(t *testing.T) {
	st := beeStateSnapshot{
		Status:     "working",
		Phase:      "running tool run_command",
		Tool:       "run_command",
		Round:      7,
		RoundsLeft: 293,
		PhaseSince: time.Now().Add(-90 * time.Second),
		UpdatedAt:  time.Now(),
	}
	line := st.humanStatus()
	for _, want := range []string{"WORKING", "round 7", "run_command", "1m 30s"} {
		if !strings.Contains(line, want) {
			t.Errorf("humanStatus = %q, want %q", line, want)
		}
	}
	if st.stalled() {
		t.Errorf("fresh state must not be stalled")
	}
}

// TestHumanStatusFinished — «закончил» отдельным явным статусом.
func TestHumanStatusFinished(t *testing.T) {
	st := beeStateSnapshot{Status: "idle", Round: 12, Reason: "final answer"}
	line := st.humanStatus()
	if !strings.Contains(line, "FINISHED") || !strings.Contains(line, "final answer") {
		t.Fatalf("humanStatus = %q, want FINISHED + reason", line)
	}
}

// TestHumanStatusErrorAndMaxRounds — падение и лимит раундов видны явно.
func TestHumanStatusErrorAndMaxRounds(t *testing.T) {
	if got := (beeStateSnapshot{Status: "error", Reason: "http 500"}).humanStatus(); !strings.Contains(got, "FAILED") || !strings.Contains(got, "http 500") {
		t.Errorf("error status = %q", got)
	}
	if got := (beeStateSnapshot{Status: "max-rounds", Reason: "max rounds (300) reached"}).humanStatus(); !strings.Contains(got, "STOPPED") {
		t.Errorf("max-rounds status = %q", got)
	}
	if got := (beeStateSnapshot{Status: "exited"}).humanStatus(); !strings.Contains(got, "EXITED") {
		t.Errorf("exited status = %q", got)
	}
}

// TestStalledDetection — heartbeat старше порога = «завис».
func TestStalledDetection(t *testing.T) {
	st := beeStateSnapshot{Status: "working", UpdatedAt: time.Now().Add(-2 * beeStateStaleAfter)}
	if !st.stalled() {
		t.Fatalf("old heartbeat must be reported as stalled")
	}
}

// TestAgentStatusLineFromStateFile — строка статуса берётся из state-файла,
// а не из эвристики по пейну.
func TestAgentStatusLineFromStateFile(t *testing.T) {
	d := NewDelegate()
	session := "bee-no-such-session-state-test"
	st := beeStateSnapshot{
		Status:     "working",
		Phase:      "model call",
		Round:      2,
		RoundsLeft: 298,
		UpdatedAt:  time.Now(),
	}
	info := &agentInfo{id: "s1", session: session, task: "do things", spawnedAt: time.Now(), statePath: writeState(t, session, st)}
	if got := d.agentStatusLine(info); !strings.Contains(got, "WORKING") {
		t.Fatalf("status = %q, want WORKING (session dead but state fresh)", got)
	}

	// Мёртвая сессия + старый heartbeat → DEAD (мозг осиротел и тоже затих).
	st.UpdatedAt = time.Now().Add(-3 * beeStateStaleAfter)
	info.statePath = writeState(t, session, st)
	if got := d.agentStatusLine(info); !strings.Contains(got, "DEAD") {
		t.Fatalf("status = %q, want DEAD", got)
	}

	// Завершённый агент при мёртвой сессии → FINISHED (нормальный финал:
	// задача сделана, процесс вышел). Это не «dead» — модель не должна
	// путать успешное завершение с аварией.
	st.Status = "idle"
	st.UpdatedAt = time.Now()
	info.statePath = writeState(t, session, st)
	if got := d.agentStatusLine(info); !strings.Contains(got, "FINISHED") {
		t.Fatalf("status = %q, want FINISHED for closed session with idle state", got)
	}

	// Убитый на середине агент: сессия умерла, heartbeat затих, задача не сделана.
	st.Status = "working"
	st.UpdatedAt = time.Now().Add(-3 * beeStateStaleAfter)
	info.statePath = writeState(t, session, st)
	if got := d.agentStatusLine(info); !strings.Contains(got, "DEAD") {
		t.Fatalf("status = %q, want DEAD for killed mid-work agent", got)
	}
}

// TestAgentStatusLineOrphaned — зомби-агент: пейна нет, а процесс мозга жив.
// Такой агент должен показываться как ORPHANED, а не как нормальный FINISHED/WORKING.
func TestAgentStatusLineOrphaned(t *testing.T) {
	d := NewDelegate()
	session := "bee-no-such-session-orphan-test"

	// Живой PID: pid 1 всегда существует. Сессия мертва, задача «закончена», но мозг жив.
	st := beeStateSnapshot{Status: "idle", Round: 2, Reason: "final answer", Waiting: true,
		PID: 1, UpdatedAt: time.Now()}
	info := &agentInfo{id: "o1", session: session, task: "x", spawnedAt: time.Now(),
		statePath: writeState(t, session, st)}
	if got := d.agentStatusLine(info); !strings.Contains(got, "ORPHANED") {
		t.Fatalf("status = %q, want ORPHANED for live brain without pane", got)
	}

	// Мёртвый PID (больше дефолтного pid_max) → это нормальный финал, не зомби.
	st.PID = 4194307
	info.statePath = writeState(t, session, st)
	if got := d.agentStatusLine(info); strings.Contains(got, "ORPHANED") || !strings.Contains(got, "FINISHED") {
		t.Fatalf("status = %q, want plain FINISHED for dead brain", got)
	}

	// Работающий мозг без пейна тоже зомби.
	st.PID = 1
	st.Status = "working"
	info.statePath = writeState(t, session, st)
	if got := d.agentStatusLine(info); !strings.Contains(got, "ORPHANED") {
		t.Fatalf("status = %q, want ORPHANED for live working brain without pane", got)
	}
}

// TestAgentWorkNote — однозначная приписка для основной модели.
func TestAgentWorkNote(t *testing.T) {
	cases := map[string]string{
		"▶ WORKING (round 1)":                      "STILL WORKING",
		"✅ FINISHED (task done)":                   "HAS FINISHED",
		"💀 DEAD (tmux gone)":                       "NOT RUNNING",
		"🛑 STALLED (no hb 90s)":                    "NOT PROGRESSING",
		"⚠️ ORPHANED (... pid 42 ...): ✅ FINISHED": "ORPHANED",
	}
	for status, want := range cases {
		note := agentWorkNote(&agentInfo{}, status)
		if !strings.Contains(note, want) {
			t.Errorf("agentWorkNote(%q) = %q, want %q", status, note, want)
		}
	}
}

// TestWithStateEnv — путь state-файла и имя сессии доезжают до команды мозга.
func TestWithStateEnv(t *testing.T) {
	got := withStateEnv([]string{"/bin/bash -c 'setsid -- /usr/local/bin/bee'"}, "/tmp/x/state.json", "bee-1")
	if len(got) != 1 {
		t.Fatalf("got %v", got)
	}
	for _, want := range []string{"BEE_STATE='/tmp/x/state.json'", "BEE_SESSION='bee-1'", "/bin/bash -c"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("cmd = %q, want %q", got[0], want)
		}
	}
}

// TestStateFilePath — путь детерминирован по имени сессии (обе стороны его знают).
func TestStateFilePath(t *testing.T) {
	a, b := stateFilePath("bee-42"), stateFilePath("bee-42")
	if a != b || !strings.HasSuffix(a, "bee-42.json") {
		t.Fatalf("stateFilePath = %q / %q", a, b)
	}
}
