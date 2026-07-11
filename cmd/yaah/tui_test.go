package yaah

import (
	"testing"
)

// TestTuiCmd_isRegistered ensures the `tui` command is wired into rootCmd.
// Regression test: M7 added the TUI but forgot the `init()` AddCommand call.
func TestTuiCmd_isRegistered(t *testing.T) {
	found := false
	for _, c := range rootCmd.Commands() {
		if c.Name() == "tui" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("tui command not registered with rootCmd; got commands: %v", rootCmd.Commands())
	}
}
