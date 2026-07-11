package yaah

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/instructions"
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/repl"
	"github.com/buchenberg/yaah/internal/skills"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/types"
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
  yaah "explain this function"

List discovered skills:
  yaah skill list

Diagnose your install:
  yaah doctor

See the design plan for the full v0.1 roadmap.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	Args:          cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRoot(cmd, args)
	},
}

// runRoot handles the no-subcommand case.
//
// Zero args → start the interactive REPL.
// One+ args → one-shot prompt (M2: will call the agent loop; for now
// we just echo back and note the feature is coming).
func runRoot(cmd *cobra.Command, args []string) error {
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

// startREPL runs the interactive read-eval-print loop.
// Reads from stdin, handles slash commands, and saves history.
// The actual agent call lands in M2 — for now this is the shell.
func startREPL() error {
	// Print banner
	fmt.Print(repl.Banner(version))

	scanner := bufio.NewScanner(os.Stdin)
	// Allow long lines (up to 1MB)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for {
		fmt.Print(repl.Prompt())

		if !scanner.Scan() {
			fmt.Println()
			break
		}

		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		// Check for slash commands
		switch repl.ParseSlashCommand(input) {
		case repl.CmdExit:
			return nil
		case repl.CmdClear:
			fmt.Print("\x1b[2J\x1b[H")
			continue
		case repl.CmdHelp:
			printHelp()
			continue
		}

		// Save to history
		if err := repl.AppendHistory(input); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not save history: %v\n", err)
		}

		// Call the agent
		response, err := runAgentPrompt(input)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s\n", replYellow("error: "+err.Error()))
		} else if response != "" {
			fmt.Println(response)
			fmt.Println()
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading stdin: %w", err)
	}

	return nil
}

// runOneShot runs the agent for a single prompt and prints the response.
func runOneShot(cmd *cobra.Command, prompt string) error {
	cmd.Printf("%s\n\n", repl.Bold("yaah "+version))

	response, err := runAgentPrompt(prompt)
	if err != nil {
		return fmt.Errorf("agent error: %w", err)
	}

	cmd.Println(response)
	return nil
}

// runAgentPrompt builds the agent loop and runs it for a single prompt.
func runAgentPrompt(prompt string) (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", fmt.Errorf("config: %w", err)
	}

	// Use the first configured provider, or the default model
	provider := resolveProvider(cfg)

	// Load project instructions (AGENTS.md / CLAUDE.md)
	cwd, _ := os.Getwd()
	instrFiles := instructions.Load(cwd, cwd)
	systemPrompt := "You are yaah, a helpful AI assistant. Respond concisely."
	if formatted := instructions.FormatForSystem(instrFiles); formatted != "" {
		systemPrompt += "\n\n" + formatted
	}

	// Discover skills
	dirs := skillSearchPaths()
	discovered := skills.Discover(dirs)
	_ = discovered // skills are loaded on demand via the skill tool

	toolReg := tools.NewRegistry()
	loop := &agent.Loop{
		Provider:      provider,
		Registry:      toolReg,
		Model:         resolveModel(cfg),
		SystemPrompt:  systemPrompt,
		MaxIterations: cfg.Default.MaxIterations,
	}

	return loop.Run(context.Background(), prompt)
}

// resolveModel extracts the model name part after the provider prefix.
// "openai/gpt-4o-mini" → "gpt-4o-mini", "gpt-4o-mini" → "gpt-4o-mini".
func resolveModel(cfg *config.Config) string {
	parts := strings.SplitN(cfg.Default.Model, "/", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return cfg.Default.Model
}

// resolveProvider picks the best available provider from the config.
func resolveProvider(cfg *config.Config) agent.Provider {
	// Try to find a provider that matches the default model prefix
	modelParts := strings.SplitN(cfg.Default.Model, "/", 2)
	if len(modelParts) == 2 {
		if p, ok := cfg.Providers[modelParts[0]]; ok && isRealKey(p.APIKey) {
			return providers.NewOpenAIClient(p.BaseURL, p.APIKey)
		}
	}

	// Fall back to any configured provider
	for _, p := range cfg.Providers {
		if isRealKey(p.APIKey) {
			return providers.NewOpenAIClient(p.BaseURL, p.APIKey)
		}
	}

	// Last resort: return a stub that explains the issue
	return &noProviderStub{}
}

// isRealKey returns true if the API key looks like a real key (not empty,
// not a placeholder, not an unsubstituted env var).
func isRealKey(key string) bool {
	if key == "" || key == "(not set)" || key == "(too short)" {
		return false
	}
	if strings.Contains(key, "${") {
		return false
	}
	return true
}

// noProviderStub is returned when no valid provider is configured.
type noProviderStub struct{}

func (s *noProviderStub) Send(req types.ChatRequest) (*types.ChatResponse, error) {
	return nil, fmt.Errorf("no provider configured — run 'yaah config edit' to add one")
}

// replYellow is a quick color helper for the REPL (avoids import cycle).
func replYellow(s string) string {
	if os.Getenv("NO_COLOR") == "" {
		return "\x1b[33m" + s + "\x1b[0m"
	}
	return s
}

// printHelp displays the available slash commands.
func printHelp() {
	fmt.Printf("  %s  %s\n", repl.Bold("/exit"), repl.Dim("quit yaah"))
	fmt.Printf("  %s  %s\n", repl.Bold("/clear"), repl.Dim("clear the screen"))
	fmt.Printf("  %s  %s\n", repl.Bold("/?"), repl.Dim("show this help"))
	fmt.Println()
}
