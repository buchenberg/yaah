package yaah

import (
	"fmt"
	"os"

	"github.com/buchenberg/yaah/internal/tui2"
	"github.com/spf13/cobra"
)

// tui2Cmd launches the tview-based TUI prototype.
var tui2Cmd = &cobra.Command{
	Use:   "tui2",
	Short: "Launch the tview-based TUI prototype",
	Long: `Launch an experimental terminal UI built with github.com/rivo/tview.
This is a visual prototype — it mirrors the yaah TUI layout but is not yet
wired to the agent loop. Use it to preview the tview component arrangement.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTUI2()
	},
}

func init() {
	rootCmd.AddCommand(tui2Cmd)
}

func runTUI2() error {
	// Suppress stderr while the TUI is active to prevent bleed-through.
	origStderr := os.Stderr
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err == nil {
		os.Stderr = devNull
	}
	defer func() {
		os.Stderr = origStderr
		if devNull != nil {
			devNull.Close()
		}
	}()

	t := tui2.New()
	fmt.Fprintf(origStderr, "Starting tview TUI2 prototype... Press Ctrl+C to quit.\n")

	if err := t.Run(); err != nil {
		return fmt.Errorf("TUI2 error: %w", err)
	}

	return nil
}
