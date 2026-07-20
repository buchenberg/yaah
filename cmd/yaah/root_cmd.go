package yaah

import (
	"fmt"
	"os"
	"strings"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/repl"
	"github.com/spf13/cobra"
)

// rootCmd is the top-level `yaah` command. When invoked with no
// subcommand and no positional arguments, it starts the interactive REPL.
// When invoked with one positional argument, it runs a one-shot prompt
// (one-shot lands in M2 with the agent loop; for now it prints a notice).
var rootCmd = &cobra.Command{
	Use:   "yaah",
	Short: "Yet Another Agent Harness — a vendor-free, open-source AI agent CLI",
	Long: `yaah is a vendor-free, open-source AI agent harness that follows
the emerging cross-tool standards (~/.agents/, SKILL.md, AGENTS.md,
MCP over stdio JSON-RPC). One static Go binary, minimal config at
~/.yaah/, no required accounts, no telemetry.

Start an interactive REPL:
  yaah

Run a one-shot prompt:
  yaah "explain this function"`,

	SilenceErrors: true,
	SilenceUsage:  true,
	Args:          cobra.ArbitraryArgs,
	RunE:          runRoot,
}

func runRoot(cmd *cobra.Command, args []string) error {
	// Easter egg: `yaah yaah [yaah ...]` prints the goat (see goat.go).
	// Checked before any session setup so it stays off the hot path.
	if isAllYaahs(args) {
		cmd.Println(goatCelebration(len(args)))
		return nil
	}

	// Initialize color support
	repl.InitNoColor()

	// Ensure config exists on first run
	if err := config.CreateDefault(); err != nil {
		// Non-fatal — we can still run with built-in defaults
		fmt.Fprintf(os.Stderr, "warning: could not create default config: %v\n", err)
	}

	// One-shot mode: args present
	if len(args) > 0 {
		prompt := strings.Join(args, " ")
		return runOneShot(cmd, prompt)
	}

	// REPL mode: no args
	return startREPL()
}

// runOneShot runs the agent for a single prompt and prints the response.
func runOneShot(cmd *cobra.Command, prompt string) error {
	cmd.Printf("%s\n\n", repl.Bold("yaah "+version))

	sess, err := newAgentSession()
	if err != nil {
		return err
	}
	defer sess.close()

	cmd.Printf("\n  %s %s/%s\n\n", Dim("provider:"), sess.providerName, sess.modelName)

	response, streamed, err := sess.runPrompt(prompt)
	if err != nil {
		return fmt.Errorf("agent error: %w", err)
	}

	if !streamed {
		cmd.Println(response)
	}
	return nil
}
