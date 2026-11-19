package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Границы таймаута одной команды run_command — как у инструмента bash_long
// основного агента: дефолт 120 с, можно поднять до 900 с (~15 минут).
// BEE-CMD-BOUNDS: менять диапазон здесь.
const (
	cmdTimeoutDefault = 120 * time.Second
	cmdTimeoutMin     = 60 * time.Second
	cmdTimeoutMax     = 900 * time.Second
)

// --- персистентный bash (руки агента) ---

// shell — персистентный bash под-агента: cwd и переменные живут между вызовами.
//
// КРИТИЧНО (почему bee раньше «вылетал» как от tmux kill-server): раньше при
// таймауте в этот bash писалось `kill 0` — сигнал ВСЕЙ process group. Персистентный
// bash и сам bee жили в одной группе, поэтому таймаут команды (30 с — любая сборка)
// убивал bee, tmux-пейн закрывался вместе с сессией. Теперь:
//   - bash запускается в СВОЕЙ process group (Setpgid), bee в неё не входит;
//   - вместо `kill 0` убивается ТОЛЬКО дерево потомков шелла (см. killShellTree);
//   - после таймаута шелл перезапускается, чтобы не тащить «мусор» из
//     недописанного вывода в следующий вызов.
type shell struct {
	cmd   *exec.Cmd
	in    io.WriteCloser
	lines chan string
	done  chan struct{}
	pid   int
}

func newShell() *shell {
	s := &shell{}
	s.start()
	return s
}

// start запускает новый bash внутри уже существующей структуры (для restart).
func (s *shell) start() {
	cmd := exec.Command("bash")
	configureShellProcess(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		panic(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		panic(err)
	}
	// stderr тоже в stdout: агент должен видеть ошибки компиляции/сборки.
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		panic(err)
	}
	s.cmd = cmd
	s.pid = cmd.Process.Pid
	s.in = stdin
	s.lines = make(chan string, 256)
	s.done = make(chan struct{})
	lines, done := s.lines, s.done
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-done:
				return
			}
		}
		close(lines)
	}()
}

// restart убивает текущий bash вместе с потомками и поднимает новый.
// Нужен после таймаута: в канале могли остаться строки от недобитой команды,
// и поиск маркера в следующем вызове ушёл бы не туда.
func (s *shell) restart() {
	s.close()
	s.start()
}

// run выполняет команду в персистентном bash и возвращает её вывод и код выхода.
//
// timeout — верхняя граница ожидания (cmdTimeoutDefault, если <= 0).
// onTick (если задан) вызывается примерно раз в 10 с, пока команда ещё идёт:
// так bee может показать в пейне и в state-файле, что долгая сборка не мертва.
//
// Возвращает текст вывода, код выхода и ошибку. По таймауту шелл
// перезапускается: код -1, ошибка "command timed out".
func (s *shell) run(command string, timeout time.Duration, onTick func(elapsed, limit time.Duration)) (string, int, error) {
	if timeout <= 0 {
		timeout = cmdTimeoutDefault
	}
	n := randN()
	marker := fmt.Sprintf("__BEE_END_%d__", n)
	if _, err := s.in.Write([]byte(fmt.Sprintf("%s\necho %s $?\n", command, marker))); err != nil {
		s.restart()
		return "", -1, err
	}

	start := time.Now()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()

	var b strings.Builder
	for {
		select {
		case <-deadline.C:
			// Убиваем только дерево потомков шелла — сам bee неприкосновенен.
			killShellTree(s.pid)
			s.restart()
			return b.String() + fmt.Sprintf("\nTIMEOUT after %s", formatDur(timeout)), -1,
				fmt.Errorf("command timed out after %s", formatDur(timeout))
		case <-tick.C:
			if onTick != nil {
				onTick(time.Since(start), timeout)
			}
		case line, ok := <-s.lines:
			if !ok {
				// Шелл умер (например, команда сделала `exit`): поднимаем новый,
				// чтобы под-агент не остался без рук.
				s.restart()
				return b.String(), -1, fmt.Errorf("shell closed")
			}
			if idx := strings.Index(line, marker); idx >= 0 {
				rc := 0
				fmt.Sscanf(strings.TrimSpace(line[idx+len(marker):]), "%d", &rc)
				return b.String(), rc, nil
			}
			b.WriteString(line + "\n")
		}
	}
}

func (s *shell) close() {
	close(s.done)
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	}
}

// callTimeout разбирает необязательный аргумент "timeout" (секунды) из tool-call
// и зажимает его в [cmdTimeoutMin, cmdTimeoutMax]. Отсутствие/мусор → дефолт.
func callTimeout(args map[string]any) time.Duration {
	t := cmdTimeoutDefault
	v, ok := args["timeout"]
	if !ok {
		return t
	}
	var secs int
	switch x := v.(type) {
	case float64:
		secs = int(x)
	case int:
		secs = x
	}
	if secs <= 0 {
		return t
	}
	t = time.Duration(secs) * time.Second
	if t < cmdTimeoutMin {
		t = cmdTimeoutMin
	} else if t > cmdTimeoutMax {
		t = cmdTimeoutMax
	}
	return t
}

// formatDur — компактная запись длительности для модели и лога ("3.2s", "1m 15s").
func formatDur(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	m := int(d / time.Minute)
	s := float64((d % time.Minute).Milliseconds()) / 1000.0
	if m == 0 {
		return fmt.Sprintf("%.1fs", s)
	}
	return fmt.Sprintf("%dm %ds", m, int(s))
}

// readStdin читает строки поправок/задач со stdin (delegate feed → tmux send-keys).
func readStdin(ch chan string) {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		ch <- sc.Text()
	}
}
