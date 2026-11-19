//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// configureShellProcess запускает персистентный bash в СВОЕЙ process group.
//
// Это ключ к тому, что таймаут команды больше не убивает под-агента:
// раньше bash и bee были в одной группе, и `kill` по группе (или `kill 0`
// из самого bash) сносил bee вместе с tmux-сессией. С Setpgid группа шелла
// изолирована: сигналы внутрь неё не задевают bee.
func configureShellProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killShellTree убивает всю process group персистентного bash (кроме самого bee,
// который в неё не входит).
func killShellTree(pid int) {
	if pid <= 0 {
		return
	}
	// Отрицательный pid = вся группа процессов шелла (именно поэтому Setpgid).
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}

// killProcessTree — общий помощник: сначала kill(-pgid), затем добиваем
// оставшихся потомков по /proc (на случай, если команда сделала setsid).
func killProcessTree(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	for _, p := range processDescendants(pid) {
		_ = syscall.Kill(p, syscall.SIGKILL)
	}
}

// processDescendants — BFS по /proc: все потомки pid (родитель раньше детей).
func processDescendants(root int) []int {
	if root <= 0 {
		return nil
	}
	children := map[int][]int{}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	for _, e := range entries {
		p, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		ppid := parsePPIDFromStat(string(data))
		if ppid > 0 {
			children[ppid] = append(children[ppid], p)
		}
	}
	tree := make([]int, 0, 8)
	queue := []int{root}
	seen := map[int]bool{root: true}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, c := range children[cur] {
			if !seen[c] {
				seen[c] = true
				tree = append(tree, c)
				queue = append(queue, c)
			}
		}
	}
	return tree
}

// parsePPIDFromStat достаёт ppid (поле 4) из /proc/<pid>/stat. Поле comm может
// содержать пробелы и скобки, поэтому режем после последней ')'.
func parsePPIDFromStat(stat string) int {
	idx := strings.LastIndex(stat, ")")
	if idx < 0 || len(stat) <= idx+2 {
		return 0
	}
	fields := strings.Fields(stat[idx+2:])
	if len(fields) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(fields[1])
	return ppid
}
