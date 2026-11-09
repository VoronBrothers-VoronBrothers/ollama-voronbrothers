//go:build !windows

package readline

import (
	"syscall"

	"golang.org/x/sys/unix"
)

func setStdinNonBlocking(fdl uintptr) int {
	oldFl, _ := unix.FcntlInt(fdl, unix.F_GETFL, 0)
	_, _ = unix.FcntlInt(fdl, unix.F_SETFL, oldFl|unix.O_NONBLOCK)
	return oldFl
}

func restoreStdinFlags(fd uintptr, oldFl int) {
	_, _ = unix.FcntlInt(fd, unix.F_SETFL, oldFl)
}

func handleCharCtrlZ(fd uintptr, termios any) (string, error) {
	t := termios.(*Termios)
	if err := UnsetRawMode(fd, t); err != nil {
		return "", err
	}

	_ = syscall.Kill(0, syscall.SIGSTOP)

	// on resume...
	return "", nil
}
