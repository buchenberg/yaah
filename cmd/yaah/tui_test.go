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

// TestTui2Cmd_removed ensures the legacy experimental command name is gone
// after tui2 was promoted to `yaah tui`.
func TestTui2Cmd_removed(t *testing.T) {
	for _, c := range rootCmd.Commands() {
		if c.Name() == "tui2" {
			t.Errorf("tui2 command still registered with rootCmd; it must be removed after promotion")
		}
	}
}
