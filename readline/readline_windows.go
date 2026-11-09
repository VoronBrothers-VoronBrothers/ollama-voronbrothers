package readline

func handleCharCtrlZ(fd uintptr, state any) (string, error) {
	// not supported
	return "", nil
}

func setStdinNonBlocking(fdl uintptr) int {
	// not supported on Windows; auto-send stays disabled
	return -1
}

func restoreStdinFlags(fd uintptr, oldFl int) {}
