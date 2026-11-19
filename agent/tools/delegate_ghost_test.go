package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestParseBeeEnv(t *testing.T) {
	environ := "PATH=/usr/bin\x00HOME=/home/user\x00BEE_STATE=/tmp/bee-state/bee-x.json\x00BEE_SESSION=bee-x\x00TERM=xterm"
	statePath, session := parseBeeEnv(environ)
	if statePath != "/tmp/bee-state/bee-x.json" {
		t.Errorf("statePath = %q", statePath)
	}
	if session != "bee-x" {
		t.Errorf("session = %q", session)
	}

	// Без BEE_* — оба пустые (это не «мозг»).
	s2, sess2 := parseBeeEnv("PATH=/bin\x00TERM=dumb")
	if s2 != "" || sess2 != "" {
		t.Errorf("expected empty for plain env, got %q %q", s2, sess2)
	}

	// Пустой environ — не должно падать.
	s3, sess3 := parseBeeEnv("")
	if s3 != "" || sess3 != "" {
		t.Errorf("expected empty for empty env")
	}
}

func TestParseSession(t *testing.T) {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Skipf("no /proc: %v", err)
	}
	sess := parseSession(string(data))
	if sess <= 0 {
		t.Fatalf("parseSession(self) = %d, want > 0", sess)
	}
	if got := parseSession("1 (init.scope) S 1 1 1 0 -1 4194304"); got != 1 {
		t.Errorf("parseSession(synthetic) = %d, want 1", got)
	}
	if got := parseSession("garbage without paren"); got != 0 {
		t.Errorf("parseSession(garbage) = %d, want 0", got)
	}
}

// TestSessionPIDsNewSession — реальный сценарий stop: создаём собственную
// session (setsid), проверяем что её пиды находятся, затем убиваем.
func TestSessionPIDsNewSession(t *testing.T) {
	cmd := exec.Command("setsid", "sleep", "10")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start setsid: %v", err)
	}
	pid := cmd.Process.Pid
	defer func() { _ = exec.Command("kill", "-9", strconv.Itoa(pid)).Run() }()

	// Даём процессу дожить до session leader.
	time.Sleep(300 * time.Millisecond)
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Skipf("process exited too fast: %v", err)
	}
	sess := parseSession(string(data))
	if sess != pid {
		t.Fatalf("setsid child should lead its own session: got %d, want %d", sess, pid)
	}
	pids := sessionPIDs(sess)
	found := false
	for _, p := range pids {
		if p == pid {
			found = true
		}
	}
	if !found {
		t.Errorf("sessionPIDs(%d) does not contain %d", sess, pid)
	}
	if got := sessionPIDs(0); len(got) != 0 {
		t.Errorf("sessionPIDs(0) = %v, want empty", got)
	}
}

func TestFindGhostsNoSelf(t *testing.T) {
	self := os.Getpid()
	for _, g := range findGhosts() {
		if g.pid == self {
			t.Fatalf("findGhosts contains our own pid %d — self must be excluded", self)
		}
		if g.statePath == "" && g.session == "" {
			t.Errorf("ghost without any identifier: %+v", g)
		}
	}
}

func TestSweepGhostsIdempotent(t *testing.T) {
	d := &Delegate{agents: map[string]*agentInfo{}}
	_ = d.sweepGhosts()
	first := len(d.agents)
	_ = d.sweepGhosts() // повторный прогон не должен дублировать
	if got := len(d.agents); got != first {
		t.Fatalf("second sweep changed registry: %d -> %d", first, got)
	}
}

// TestGhostAdoptStopKillsSession — полный цикл: поддельный «мозг» (setsid +
// BEE_STATE env) с внуком в собственном pgrp. sweepGhosts adopt'ит его, stop
// должен убить и мозг, и внука (session-wide kill).
func TestGhostAdoptStopKillsSession(t *testing.T) {
	sp := filepath.Join(os.TempDir(), "bee-state", "ghost-e2e-test.json")
	_ = os.MkdirAll(filepath.Dir(sp), 0o755)

	// SysProcAttr{Setsid} — P сам становится session leader (детерминированно,
	// без util-linux-обёртки). set -m даёт внуку свой pgrp внутри сессии.
	cmd := exec.Command("/bin/bash", "-c", "set -m; sleep 120 & exec sleep 120")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = append(os.Environ(), "BEE_STATE="+sp, "BEE_SESSION=ghost-e2e-test")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start fake brain: %v", err)
	}
	P := cmd.Process.Pid

	cleanup := func() {
		for _, p := range sessionPIDs(P) {
			_ = exec.Command("kill", "-9", strconv.Itoa(p)).Run()
		}
		_ = os.Remove(sp)
	}
	defer cleanup()
	time.Sleep(400 * time.Millisecond)
	if procState(P) == "" {
		t.Skipf("fake brain died too fast")
	}

	d := &Delegate{agents: map[string]*agentInfo{}}
	if n := d.sweepGhosts(); n == 0 {
		t.Fatalf("sweepGhosts did not adopt the fake brain (pid %d)", P)
	}
	// Ищем adopted-агента, чей pid сам ведёт свою сессию (session leader).
	// Внуки наследуют BEE_STATE env и тоже adopt'ятся — это нормально,
	// но stop нужен именно по лидеру.
	id := ""
	d.mu.Lock()
	for k, a := range d.agents {
		if a.statePath == sp {
			if data, err := os.ReadFile("/proc/" + strconv.Itoa(a.brainPid) + "/stat"); err == nil && parseSession(string(data)) == a.brainPid {
				id = k
			}
		}
	}
	d.mu.Unlock()
	if id == "" {
		t.Fatalf("fake brain not in registry: %+v", d.agents)
	}

	res, err := d.stop(id)
	if err != nil {
		t.Fatalf("stop returned error: %v", res)
	}
	time.Sleep(200 * time.Millisecond)
	_ = cmd.Wait() // собрать zombie, чтобы не мешал проверке
	if procAlive(P) {
		t.Errorf("brain pid %d still alive after stop: %s", P, res.Content)
	}
	// Внуки в той же session (свои pgrp) тоже должны быть мертвы.
	for _, p := range sessionPIDs(P) {
		if procAlive(p) {
			t.Errorf("session member pid %d survived stop", p)
		}
	}
	t.Logf("stop result: %s", res.Content)
}
