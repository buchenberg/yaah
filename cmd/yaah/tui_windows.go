//go:build windows

package yaah

// installSignalHandlers is a no-op on Windows (no SIGTSTP/SIGCONT).
func installSignalHandlers() func() {
	return func() {}
}
