// selfsay — отправляет текст во ввод живого процесса (например, своего же REPL).
//
// Примеры:
//
//	selfsay -list                       # показать кандидатов
//	selfsay "текст"                     # отправить текстовую реплику
//	selfsay -buf                        # отправить содержимое буфера X (xclip)
//	selfsay -pts /dev/pts/3 "текст"     # точная цель
//	cat file.txt | selfsay -            # из stdin
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type candidate struct {
	pid int
	pts string
	cmd string
}

var candidates []candidate

func main() {
	var (
		match = "ollama"
		pts   string
		buf   bool
		noNL  bool
		list  bool
	)

	var rest []string
	for _, arg := range os.Args[1:] {
		switch {
		case arg == "-list":
			list = true
		case strings.HasPrefix(arg, "-pts="):
			pts = strings.TrimPrefix(arg, "-pts=")
		case strings.HasPrefix(arg, "-match="):
			match = strings.TrimPrefix(arg, "-match=")
		case arg == "-buf":
			buf = true
		case arg == "-noenter":
			noNL = true
		default:
			rest = append(rest, arg)
		}
	}

	text := ""
	if buf {
		out, err := exec.Command("xclip", "-selection", "clipboard", "-o").Output()
		if err != nil {
			fmt.Fprintf(os.Stderr, "selfsay: xclip: %v\n", err)
			os.Exit(1)
		}
		text = string(out)
	} else if len(rest) > 0 {
		if rest[0] == "-" {
			data, _ := io.ReadAll(os.Stdin)
			text = string(data)
		} else {
			text = strings.Join(rest, " ")
		}
	}

	var err error
	if candidates, err = findCandidates(match); err != nil {
		fatal(err)
	}
	if list {
		for _, c := range candidates {
			fmt.Printf("%d  %s  %s\n", c.pid, c.pts, c.cmd)
		}
		return
	}
	if len(candidates) == 0 {
		fatal(fmt.Errorf("нет процессов с match=%q и fd/0 -> pts", match))
	}

	var target string
	if pts != "" {
		target = pts
	} else {
		target = candidates[len(candidates)-1].pts // самый новый pid
	}

	// Пишем в master-сторону pty (fd процесса-терминала, указывающий на pts),
	// а не в slave /dev/pts/N — тот просто выведет текст на экран.
	writer := findMaster(target)
	if writer == "" {
		fatal(fmt.Errorf("никто не держит master для %s", target))
	}

	payload := text
	if !strings.HasSuffix(payload, "\n") {
		payload += "\n"
	}
	if !noNL {
		payload = strings.ReplaceAll(payload, "\n", "\r\n")
	}

	f, err := os.OpenFile(writer, os.O_WRONLY, 0)
	if err != nil {
		fatal(fmt.Errorf("открыть %s: %w", writer, err))
	}
	defer f.Close()
	if _, err := f.Write([]byte(payload)); err != nil {
		fatal(err)
	}
	fmt.Fprintf(os.Stderr, "selfsay: %d байт -> %s (через %s)\n", len(payload), target, writer)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "selfsay:", err)
	os.Exit(1)
}

// findCandidates ищет процессы с подстрокой match в cmdline,
// у которых fd/0 указывает на /dev/pts/*.
func findCandidates(match string) ([]candidate, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []candidate
	for _, e := range entries {
		if !isDigit(e.Name()) {
			continue
		}
		pid, _ := strconv.Atoi(e.Name())
		cmdline, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil {
			continue
		}
		cmd := strings.TrimSpace(strings.ReplaceAll(string(cmdline), "\x00", " "))
		if !strings.Contains(cmd, match) {
			continue
		}
		p, err := os.Readlink("/proc/" + e.Name() + "/fd/0")
		if err != nil || !strings.HasPrefix(p, "/dev/pts/") {
			continue
		}
		out = append(out, candidate{pid: pid, pts: p, cmd: cmd})
	}
	for i := 1; i < len(out); i++ {
		if out[i].pid > out[i-1].pid {
			out[i-1], out[i] = out[i], out[i-1]
		}
	}
	return out, nil
}

// findMaster ищет fd процесса (не самого match-процесса), указывающий на pts —
// это master-сторона псевдотерминала; запись в неё = ввод для процесса.
func findMaster(pts string) string {
	exclude := map[int]bool{}
	for _, c := range candidates {
		exclude[c.pid] = true
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !isDigit(e.Name()) {
			continue
		}
		pid, _ := strconv.Atoi(e.Name())
		if exclude[pid] {
			continue
		}
		// slave-держатель (fd/0 -> тот же pts) не подходит
		if link, err := os.Readlink("/proc/" + e.Name() + "/fd/0"); err == nil && link == pts {
			continue
		}
		fds, err := os.ReadDir("/proc/" + e.Name() + "/fd")
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink("/proc/" + e.Name() + "/fd/" + fd.Name())
			if err == nil && link == pts {
				return "/proc/" + e.Name() + "/fd/" + fd.Name()
			}
		}
	}
	return ""
}

func isDigit(s string) bool {
	return len(s) > 0 && s[0] >= '0' && s[0] <= '9'
}
