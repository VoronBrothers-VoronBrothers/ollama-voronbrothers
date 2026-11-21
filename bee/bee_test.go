package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCallTimeoutClamp — аргумент timeout зажимается как у bash_long:
// дефолт 120с, минимум 60с, максимум 900с, мусор → дефолт.
func TestCallTimeoutClamp(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want time.Duration
	}{
		{"absent", map[string]any{}, cmdTimeoutDefault},
		{"in range", map[string]any{"timeout": float64(300)}, 300 * time.Second},
		{"too small", map[string]any{"timeout": float64(5)}, cmdTimeoutMin},
		{"too big", map[string]any{"timeout": float64(5000)}, cmdTimeoutMax},
		{"zero", map[string]any{"timeout": float64(0)}, cmdTimeoutDefault},
		{"negative", map[string]any{"timeout": -10}, cmdTimeoutDefault},
		{"int type", map[string]any{"timeout": 120}, 120 * time.Second},
		{"junk", map[string]any{"timeout": "soon"}, cmdTimeoutDefault},
	}
	for _, c := range cases {
		if got := callTimeout(c.args); got != c.want {
			t.Errorf("%s: callTimeout = %s, want %s", c.name, got, c.want)
		}
	}
}

// TestFormatDur — компактная запись длительности.
func TestFormatDur(t *testing.T) {
	cases := map[time.Duration]string{
		500 * time.Millisecond: "500ms",
		3 * time.Second:        "3.0s",
		75 * time.Second:       "1m 15s",
	}
	for in, want := range cases {
		if got := formatDur(in); got != want {
			t.Errorf("formatDur(%s) = %q, want %q", in, got, want)
		}
	}
}

// TestStateFileRoundTrip — bee пишет state-файл атомарно, поля доходят.
func TestStateFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	t.Setenv("BEE_STATE", path)
	t.Setenv("BEE_SESSION", "bee-test")

	initStateFile("test/model")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file not created: %v", err)
	}

	updateState(func(s *beeState) {
		s.Status = statusWorking
		s.Phase = "model call"
		s.Round = 3
		s.RoundsLeft = 297
	})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got beeState
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, data)
	}
	if got.Status != statusWorking || got.Phase != "model call" || got.Round != 3 || got.RoundsLeft != 297 {
		t.Fatalf("unexpected state: %+v", got)
	}
	if got.Session != "bee-test" {
		t.Fatalf("session = %q, want bee-test", got.Session)
	}
	if got.PID != os.Getpid() {
		t.Fatalf("pid = %d, want %d", got.PID, os.Getpid())
	}
	if time.Since(got.UpdatedAt) > time.Minute {
		t.Fatalf("updated_at not fresh: %s", got.UpdatedAt)
	}

	// PhaseSince должен обновляться только при смене фазы.
	prev := got.PhaseSince
	updateState(func(s *beeState) { s.Round = 4 })
	data, _ = os.ReadFile(path)
	_ = json.Unmarshal(data, &got)
	if !got.PhaseSince.Equal(prev) {
		t.Errorf("phase_since changed without phase change: %s → %s", prev, got.PhaseSince)
	}
	updateState(func(s *beeState) { s.Phase = "run_command" })
	data, _ = os.ReadFile(path)
	_ = json.Unmarshal(data, &got)
	if !got.PhaseSince.After(prev) {
		t.Errorf("phase_since should advance on phase change")
	}
}

// TestStateHeartbeat — heartbeat обновляет UpdatedAt.
func TestStateHeartbeat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	t.Setenv("BEE_STATE", path)
	initStateFile("m")
	before := beeStateFile.st.UpdatedAt
	time.Sleep(5 * time.Millisecond)
	touchState()
	if !beeStateFile.st.UpdatedAt.After(before) {
		t.Fatalf("touchState did not refresh updated_at")
	}
}

// TestShellRunLongCommand — долгая команда (> старые 30 с) не убивает bee:
// персистентный шелл живёт в своей process group, а run() получает нужный timeout.
func TestShellRunLongCommand(t *testing.T) {
	sh := newShell()
	defer sh.close()

	out, rc, err := sh.run("echo hello", 0, nil)
	if err != nil || rc != 0 {
		t.Fatalf("run echo: rc=%d err=%v out=%q", rc, err, out)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("out = %q, want hello", out)
	}

	// cwd сохраняется между вызовами.
	if _, _, err := sh.run("cd /tmp", 0, nil); err != nil {
		t.Fatalf("cd: %v", err)
	}
	out, _, _ = sh.run("pwd", 0, nil)
	if !strings.Contains(out, "/tmp") {
		t.Fatalf("pwd = %q, want persistent /tmp", out)
	}
}

// TestShellTimeoutKeepsProcessAlive — таймаут убивает ТОЛЬКО потомков шелла
// (а не process group целиком, как раньше `kill 0`), и шелл перезапускается.
func TestShellTimeoutKeepsProcessAlive(t *testing.T) {
	sh := newShell()
	defer sh.close()
	pidBefore := sh.pid

	_, rc, err := sh.run("sleep 30", 1200*time.Millisecond, func(elapsed, limit time.Duration) {
		t.Logf("tick: %s / %s", elapsed, limit)
	})
	if err == nil || rc != -1 {
		t.Fatalf("want timeout error, got rc=%d err=%v", rc, err)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want timed out", err)
	}
	// Шелл перезапущен — новый pid, и он работает.
	if sh.pid == pidBefore {
		t.Errorf("shell should be restarted after timeout")
	}
	out, rc, err := sh.run("echo alive", 0, nil)
	if err != nil || rc != 0 || !strings.Contains(out, "alive") {
		t.Fatalf("shell unusable after timeout: rc=%d err=%v out=%q", rc, err, out)
	}
}

// TestRunCommandPrefix — результат run_command содержит префикс с временем,
// длительностью и дельтой до предыдущего вызова (как bash_long).
func TestRunCommandPrefix(t *testing.T) {
	lastRunAt = time.Time{}
	sh := newShell()
	defer sh.close()

	res := runCommand(sh, map[string]any{"command": "true"})
	if !strings.HasPrefix(res, "[Time: ") {
		t.Fatalf("res = %q, want Time prefix", res)
	}
	if !strings.Contains(res, "Duration: ") || !strings.Contains(res, "exit=0") {
		t.Fatalf("res = %q, want Duration and exit code", res)
	}
	res2 := runCommand(sh, map[string]any{"command": "true"})
	if !strings.Contains(res2, "Since last run_command") {
		t.Fatalf("second call = %q, want Since last run_command", res2)
	}
}

// TestRunTaskWritesStates — сквозной прогон tool-loop против фейкового
// OpenAI-совместимого сервера: state-файл должен пройти working → idle,
// а run_command — получить timeout из аргументов (как bash_long).
func TestRunTaskWritesStates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	t.Setenv("BEE_STATE", path)
	initStateFile("fake/model")

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Messages []chatMsg `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			// Просим выполнить короткую команду с длинным timeout.
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"",
				"tool_calls":[{"id":"c1","type":"function","function":{"name":"run_command",
				"arguments":"{\"command\":\"echo hi\",\"timeout\":600}"}}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"all done"}}]}`))
	}))
	defer srv.Close()

	ep := endpoint{baseURL: srv.URL, provider: "fake"}
	sh := newShell()
	defer sh.close()
	runTask(&http.Client{Timeout: 10 * time.Second}, ep, sh, make(chan string), "say hi")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	var st beeState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if st.Status != statusIdle {
		t.Fatalf("status = %q, want %q (task finished)", st.Status, statusIdle)
	}
	if st.Reason != "final answer" {
		t.Fatalf("reason = %q, want final answer", st.Reason)
	}
	if st.Round != 2 {
		t.Fatalf("round = %d, want 2 (tool call + final answer)", st.Round)
	}
	if st.RoundsLeft != maxRoundsN-1 {
		t.Fatalf("rounds_left = %d, want %d", st.RoundsLeft, maxRoundsN-1)
	}
}

// TestRejectSuicidalCommand — команды, которые сносят tmux-сервер или сам bee,
// отклоняются (это была вторая причина «под-агент вылетел как от kill-server»).
func TestRejectSuicidalCommand(t *testing.T) {
	bad := []string{
		"tmux kill-server",
		"tmux  kill-server; echo done",
		"killall tmux",
		"pkill -f tmux",
		"kill 0",
		"kill -9 0",
		"kill -1",
		"killall bash",
		"pkill bee",
		"pkill -x bee",
	}
	for _, cmd := range bad {
		if err := rejectSuicidalCommand(cmd); err == nil {
			t.Errorf("command %q should be rejected", cmd)
		}
	}
	good := []string{
		"tmux kill-session -t bee-1",
		"tmux ls",
		"ps aux | grep tmux",
		"go build ./...",
		"kill 12345",
	}
	for _, cmd := range good {
		if err := rejectSuicidalCommand(cmd); err != nil {
			t.Errorf("command %q should be allowed, got %v", cmd, err)
		}
	}

	// tmux kill-session -t <own> должен быть заблокирован при заданном BEE_SESSION.
	t.Setenv("BEE_SESSION", "bee-123")
	selfKill := []string{
		"tmux kill-session -t bee-123",
		"tmux kill-pane -t bee-123",
		"tmux kill-session -t $BEE_SESSION",
		"tmux kill-session -t \"$BEE_SESSION\"",
	}
	for _, cmd := range selfKill {
		if err := rejectSuicidalCommand(cmd); err == nil {
			t.Errorf("command %q should be rejected (targets own session)", cmd)
		}
	}
	// Чужая сессия — разрешена.
	if err := rejectSuicidalCommand("tmux kill-session -t bee-999"); err != nil {
		t.Errorf("tmux kill-session targeting OTHER session should be allowed, got %v", err)
	}
}
