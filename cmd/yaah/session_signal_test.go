package yaah

import (
	"os"
	"testing"
	"time"
)

// TestTerminationSecondSignalExitsWhileClosing pins that signals stay an escape
// hatch: teardown is bounded but can take up to a minute (three drains), and the
// first Ctrl-C only starts it. A second one must exit even while close is still
// blocked — otherwise the funnel turns an impatient user's Ctrl-C into a
// hang, which is the opposite of what a signal is for.
func TestTerminationSecondSignalExitsWhileClosing(t *testing.T) {
	sigs := make(chan os.Signal, 2)
	release := make(chan struct{})
	exits := make(chan int, 4)
	closed := make(chan struct{})

	go handleTerminationSignal(sigs,
		func() {
			close(closed)
			<-release // a teardown that has not finished
		},
		func(code int) { exits <- code })

	sigs <- os.Interrupt
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("the first signal did not start the teardown")
	}

	sigs <- os.Interrupt
	select {
	case code := <-exits:
		if code != interruptExitCode {
			t.Errorf("exit code = %d; want %d", code, interruptExitCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a second signal did not exit while teardown was still running")
	}
	close(release)
}
