package yaah

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/repl"
	"github.com/spf13/cobra"
)

// rootCmd is the top-level `yaah` command. When invoked with no
// subcommand and no positional arguments, it starts the interactive REPL.
// With one positional argument, it runs a one-shot prompt.
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
		if err := maybeResolveResumePicker(os.Stdout, os.Stdin); err != nil {
			return err
		}
		opts := sessionOptionsFromFlags()
		opts.OneShot = true
		return runOneShotWith(cmd, prompt, opts)
	}

	// REPL mode: no args
	if err := maybeResolveResumePicker(os.Stdout, os.Stdin); err != nil {
		return err
	}
	return startREPL()
}

// runOneShotWith runs the agent for a single prompt and prints the
// response. One-shot sessions are non-interactive: the opts carry the
// OneShot marker so the wiring adds the non-interactivity guidance and
// leaves the question tool out of the belt.
func runOneShotWith(cmd *cobra.Command, prompt string, opts SessionOptions) error {
	cmd.Printf("%s\n\n", repl.Bold("yaah "+version))

	sess, err := newAgentSessionWithOptions(opts, false, false)
	if err != nil {
		return err
	}
	defer sess.close()

	cmd.Printf("\n  %s %s/%s\n\n", Dim("provider:"), sess.ProviderName(), sess.ModelName())

	tv := newTerminalView()
	tv.start()
	sess.SetView(tv)
	response, streamed, err := sess.RunPrompt(context.Background(), prompt)
	if err != nil {
		return fmt.Errorf("agent error: %w", err)
	}

	if !streamed {
		cmd.Println(response)
	}
	return nil
}
