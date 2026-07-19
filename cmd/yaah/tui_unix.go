//go:build !windows

package yaah

import (
	"os"
	"os/signal"
	"syscall"
)

// installSignalHandlers installs SIGTSTP/SIGCONT handlers so the Go runtime
// doesn't block suspend/resume events before Bubble Tea v2 sees them.
// Returns a stop function that should be deferred.
func installSignalHandlers() func() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTSTP, syscall.SIGCONT)
	go func() {
		for range sigCh {
			// Bubble Tea v2's internal signal handlers restore and re-enter
			// raw mode / alt screen automatically.
		}
	}()
	return func() {
		signal.Stop(sigCh)
		close(sigCh)
	}
}
