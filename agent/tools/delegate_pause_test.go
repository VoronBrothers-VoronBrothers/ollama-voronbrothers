package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParsePPID(t *testing.T) {
	cases := []struct {
		name string
		stat string
		want int
	}{
		{"simple", "1234 (sleep) S 1 1234 1234", 1},
		{"comm with spaces and parens", "567 (bash -c weird (stuff)) R 42 567 567", 42},
		{"no close paren", "999 broken", 0},
		{"too short after paren", "1 (x)", 0},
	}
	for _, c := range cases {
		if got := parsePPID(c.stat); got != c.want {
			t.Errorf("%s: parsePPID(%q) = %d, want %d", c.name, c.stat, got, c.want)
		}
	}
}

// pidState — состояние процесса из /proc/<pid>/stat ('R','S','T',...). Пусто если умер.
func pidState(t *testing.T, pid int) string {
	t.Helper()
	return procState(pid)
}

// waitForState — поллинг состояния pid до want (или таймаут).
func waitForState(t *testing.T, pid int, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := pidState(t, pid); got == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("pid %d: state never became %q (last=%q)", pid, want, pidState(t, pid))
}

// holdState — состояние pid ДОЛЖНО оставаться want в течение всего duration.
// Это проверка на «выдержку паузы»: tmux-сервер шлёт SIGCONT процессам пейна
// почти сразу после STOP; setsid-детаченный brain должен пережить эту волну.
func holdState(t *testing.T, pid int, want string, duration time.Duration) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		if got := pidState(t, pid); got != want {
			t.Fatalf("pid %d: state became %q during required %q (pause not holding)", pid, got, want)
		}
		time.Sleep(80 * time.Millisecond)
	}
}

// TestPauseResumeFreezeWholeTree — e2e: tmux-сессия с деревом процессов:
// root bash + два setsid-детаченных sleep (имитация «мозга» через setsid, как
// в agentCommand). pause() должен заморозить brain насовсем (насколько tmux
// позволяет), resume() — вернуть.
func TestPauseResumeFreezeWholeTree(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	session := "bee-test-pause"
	exec.Command("tmux", "kill-session", "-t", session).Run() // мусор от упавших прогонов
	// setsid БЕЗ --fork: процесс остаётся живым ребёнком bash (в дереве пейна),
	// но в СОБСТВЕННОЙ сессии — поэтому авто-SIGCONT от tmux его не расталкивает.
	// --fork репарентил бы детей к systemd и они выпали бы из дерева.
	// `sleep 45` держит пейн живым (сами setsid-обёртки завершаются сразу).
	cmd := exec.Command("tmux", "new-session", "-d", "-s", session, "-c", "/tmp", "--",
		"bash", "-c", `setsid sleep 300 & setsid sleep 301 & sleep 45`)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v\n%s", err, out)
	}
	defer exec.Command("tmux", "kill-session", "-t", session).Run()

	time.Sleep(400 * time.Millisecond) // дать пейну подняться

	d := NewDelegate()
	root := panePIDOf(session)
	if root <= 0 {
		t.Fatal("pane_pid not found")
	}
	tree := processTree(root)
	brains := make([]int, 0, 2)
	for _, pid := range tree[1:] { // root не brain
		if strings.Contains(readCmdline(pid), "sleep 3") { // sleep 300/301, но не keep-alive
			brains = append(brains, pid)
		}
	}
	if len(brains) != 2 {
		t.Fatalf("expected 2 detached sleep brains in tree %v (cmdline of kids: %v)", tree, kidComms(tree))
	}

	info := &agentInfo{id: "t1", session: session, panePID: root}
	d.mu.Lock()
	d.agents["t1"] = info
	d.mu.Unlock()

	// PAUSE: brain (setsid) обязан продержаться в 'T' несмотря на SIGCONT от tmux.
	if res, _ := d.pause("t1"); !strings.Contains(res.Content, "Paused") {
		t.Fatalf("pause result unexpected: %s", res.Content)
	}
	for _, pid := range brains {
		waitForState(t, pid, "T")
	}
	for _, pid := range brains {
		holdState(t, pid, "T", 700*time.Millisecond) // tmux успевает «отстреляться» за ~100мс
	}

	// RESUME: все brain должны вернуться из 'T'.
	if res, _ := d.resume("t1"); !strings.Contains(res.Content, "Resumed") {
		t.Fatalf("resume result unexpected: %s", res.Content)
	}
	for _, pid := range brains {
		waitForState(t, pid, "S")
	}

	// Пауза несуществующего агента — мягкая ошибка.
	if res, _ := d.pause("nope"); !strings.Contains(res.Content, "Error") {
		t.Fatalf("want error for unknown agent, got: %s", res.Content)
	}
}

func readCmdline(pid int) string {
	data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	return strings.ReplaceAll(string(data), "\x00", " ")
}

func kidComms(tree []int) (out []string) {
	for _, p := range tree[1:] {
		out = append(out, readCmdline(p))
	}
	return out
}

// TestAgentCommandSetsidWrap — «мозг» всегда запускается через setsid, чтобы
// pause() (SIGSTOP) переживал авто-SIGCONT tmux-сервера.
func TestAgentCommandSetsidWrap(t *testing.T) {
	t.Setenv("BEE_AGENT_CMD", "bash -c 'echo brain'")
	got := agentCommand("task text", "")
	// Форма: ["/bin/bash -c 'setsid -- <quoted args>'"] — см. agentCommand:
	// setsid НЕ может быть прямым ребёнком tmux-пейна (EPERM → fork → пейн умирает).
	if len(got) != 1 || !strings.HasPrefix(got[0], "/bin/bash -c ") {
		t.Fatalf("agentCommand must be a single bash -c line, got: %v", got)
	}
	rest := got[0]
	if !strings.Contains(rest, "setsid --") || !strings.Contains(rest, "echo") {
		t.Errorf("brain args lost after wrap: %q", rest)
	}

	// Без BEE_AGENT_CMD и без bee-бинарника — placeholder bash, тоже в setsid.
	t.Setenv("BEE_BIN", "/nonexistent-bee-binary")
	os.Unsetenv("BEE_AGENT_CMD")
	got = agentCommand("task", "")
	if len(got) != 1 || !strings.Contains(got[0], "setsid --") {
		t.Fatalf("placeholder must be setsid-wrapped too, got: %v", got)
	}
	if !strings.Contains(got[0], "bash") {
		t.Errorf("placeholder brain lost: %v", got)
	}
}

// TestSpawnPauseE2e — полный цикл через spawn(): агент реально пишет строки в
// лог; после pause() лог ЗАСТАВАЕТ расти (было: писал даже на паузе), после
// resume() — продолжает до конца. Это и есть первоначальный баг из репорта.
func TestSpawnPauseE2e(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	tmp := t.TempDir()
	log := filepath.Join(tmp, "work.log")

	const total = 6 // строк напишет brain
	// seq: bash-`for i in 1 2 N` это список {1,2,N}, а не диапазон!
	cmdline := fmt.Sprintf(
		"for i in $(seq 1 %d); do echo $i >> %s; sleep 0.8; done", total, log)
	t.Setenv("BEE_AGENT_CMD", cmdline)

	d := NewDelegate()
	// Гарантируем живой tmux-сервер: предыдущий тест мог убить последнюю
	// сессию → сервер умер → new-session в spawn'е ругается на stale socket.
	exec.Command("tmux", "new-session", "-d", "-s", "bee-e2e-keeper").Run()
	defer exec.Command("tmux", "kill-session", "-t", "bee-e2e-keeper").Run()

	id := "e2epause"
	if res, _ := d.spawn(tmp, "write lines to log", "", id); !strings.Contains(res.Content, "Spawned") {
		t.Fatalf("spawn failed: %s", res.Content)
	}
	defer func() {
		d.stop(id)
		exec.Command("tmux", "kill-session", "-t", "bee-"+id).Run()
	}()

	// Ждём, что brain начал работать (>=2 строки — пауза не в первый миг).
	deadline := time.Now().Add(8 * time.Second)
	for {
		if countLines(log) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("brain did not start working; log=%q", mustRead(log))
		}
		time.Sleep(100 * time.Millisecond)
	}

	before := countLines(log)
	if res, _ := d.pause(id); !strings.Contains(res.Content, "Paused") {
		t.Fatalf("pause failed: %s", res.Content)
	}

	// Пауза должна УДЕРЖАТЬСЯ: 2.5с — это ~3 итерации brain'а по 0.8с.
	time.Sleep(2500 * time.Millisecond)
	during := countLines(log)
	if during > before+1 { // +1: окно гонки «тест прочитал лог → SIGSTOP root» ≤ одной итерации
		t.Fatalf("agent kept working during pause: %d -> %d lines (log=%q)", before, during, mustRead(log))
	}

	if res, _ := d.resume(id); !strings.Contains(res.Content, "Resumed") {
		t.Fatalf("resume failed: %s", res.Content)
	}

	// После resume brain дописывает остаток.
	deadline = time.Now().Add(10 * time.Second)
	for {
		if countLines(log) >= total {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent did not finish after resume: %d/%d lines (log=%q)", countLines(log), total, mustRead(log))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func countLines(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

func mustRead(path string) string {
	data, _ := os.ReadFile(path)
	return string(data)
}

// TestProcessTreeIncludesLiveChildren — процессное дерево живого шелла должно
// включать root и его реальных детей; pids <= 0 не должны попадать в kill.
func TestProcessTreeIncludesLiveChildren(t *testing.T) {
	cmd := exec.Command("bash", "-c", "sleep 30 & sleep 31 & wait")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	time.Sleep(400 * time.Millisecond)

	tree := processTree(cmd.Process.Pid)
	set := map[int]bool{}
	for _, p := range tree {
		set[p] = true
	}
	if !set[cmd.Process.Pid] {
		t.Errorf("tree must include root pid %d, got %v", cmd.Process.Pid, tree)
	}
	out, _ := exec.Command("pgrep", "-P", strconv.Itoa(cmd.Process.Pid)).Output()
	kids := strings.Fields(strings.TrimSpace(string(out)))
	if len(kids) == 0 {
		t.Fatal("bash child not found")
	}
	kpid, _ := strconv.Atoi(kids[0])
	if !set[kpid] {
		t.Errorf("tree must include live child %d, got %v", kpid, tree)
	}
}

// TestListShowsPausedStatus — list() обязан ЧЕТКО показывать паузу:
// frozen pids → "⏸ PAUSED (frozen N pid)", сессия мертва → "(session gone)".
func TestListShowsPausedStatus(t *testing.T) {
	d := NewDelegate()
	info := &agentInfo{id: "pl", session: "bee-no-such-session-pl", panePID: 999999, spawnedAt: time.Now()}
	d.agents["pl"] = info

	// Не на паузе + сессия мертва → dead.
	res, _ := d.list()
	if !strings.Contains(res.Content, "💀 DEAD") {
		t.Fatalf("want DEAD status, got: %s", res.Content)
	}

	// На паузе, но никто не застыл и сессия мертва → paused (session gone).
	info.paused = true
	info.stoppedPids = []int{999999}
	res, _ = d.list()
	if !strings.Contains(res.Content, "⏸") {
		t.Fatalf("want paused status, got: %s", res.Content)
	}

	// Реальный freeze ЖЕРТВЫ (не себя! SIGSTOP себя = deadlock: замороженный
	// тест не успеет отправить CONT): child sleep → T → list видит "frozen".
	helper := exec.Command("sleep", "30")
	if err := helper.Start(); err != nil {
		t.Skipf("cannot start helper: %v", err)
	}
	defer func() {
		_ = exec.Command("kill", "-CONT", strconv.Itoa(helper.Process.Pid)).Run()
		_ = helper.Process.Kill()
	}()
	if err := exec.Command("kill", "-STOP", strconv.Itoa(helper.Process.Pid)).Run(); err != nil {
		t.Skipf("SIGSTOP helper: %v", err)
	}
	if procState(helper.Process.Pid) != "T" {
		t.Skip("helper did not freeze")
	}
	info.stoppedPids = []int{helper.Process.Pid}
	res, _ = d.list()
	if !strings.Contains(res.Content, "⏸ PAUSED (frozen 1 pid)") {
		t.Fatalf("want frozen status, got: %s", res.Content)
	}
}
