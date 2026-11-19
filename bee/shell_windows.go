//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// configureShellProcess: на Windows у процессов нет process group в POSIX-смысле;
// CREATE_NEW_PROCESS_GROUP даёт отдельную группу, чтобы Ctrl-Break/сигналы,
// адресованные пейну, не задевали сам bee.
func configureShellProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// killShellTree завершает дерево процессов шелла (taskkill /T — «с потомками»).
func killShellTree(pid int) {
	if pid <= 0 {
		return
	}
	_ = exec.Command("taskkill", "/F", "/T", "/PID", itoa(pid)).Run()
}

// killProcessTree — то же, что killShellTree (Windows-дерево через taskkill).
func killProcessTree(pid int) { killShellTree(pid) }

// processDescendants на Windows не нужен: taskkill /T сам обходит дерево.
func processDescendants(root int) []int { return nil }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
