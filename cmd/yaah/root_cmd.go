package yaah

import (
	"github.com/spf13/cobra"
)

// rootCmd is the top-level `yaah` command. When invoked with no
// subcommand and no positional argument, it starts the interactive REPL
// (M1). When invoked with one positional argument, it runs a one-shot
// prompt (M1). Subcommands handle `yaah skill ...`, `yaah mcp ...`,
// `yaah memory ...`, `yaah session ...`, `yaah config ...`, etc.
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
  yaah "explain this function"

List discovered skills:
  yaah skill list

Diagnose your install:
  yaah doctor

See the design plan for the full v0.1 roadmap.`,
	SilenceUsage:  true, // don't dump --help on a runtime error
	SilenceErrors: true, // main.go prints the error
	Args:          cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Subcommand dispatch is delegated to RunE so we can branch on
		// the args. In v0.0.0 we just print a placeholder; the real
		// REPL and one-shot paths land in M1.
		return runRoot(cmd, args)
	},
}

// runRoot handles the no-subcommand case.
//
// In v0.0.0: print a friendly "not yet implemented" banner.
// In v0.1 M1: start the REPL (zero args) or run a one-shot prompt (one arg).
func runRoot(cmd *cobra.Command, args []string) error {
	cmd.Println("yaah", version)
	cmd.Println()
	cmd.Println("This is a v0.0.0 placeholder build.")
	cmd.Println("The interactive REPL and one-shot prompt land in Milestone 1.")
	cmd.Println()
	cmd.Println("Try one of:")
	cmd.Println("  yaah --help          full help")
	cmd.Println("  yaah skill list      list discovered skills (M3)")
	cmd.Println("  yaah doctor          diagnose your install (M1)")
	cmd.Println("  yaah update --check  check for a newer release")
	return nil
}
