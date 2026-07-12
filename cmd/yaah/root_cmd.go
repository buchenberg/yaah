package yaah

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/instructions"
	"github.com/buchenberg/yaah/internal/mcp"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/repl"
	"github.com/buchenberg/yaah/internal/spinner"
	"github.com/buchenberg/yaah/internal/todo"
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
// Builds infrastructure (config, provider, tools, DB, MCP) once per session
// and reuses it across prompts.
func startREPL() error {
	fmt.Print(repl.Banner(version))

	// Build the agent session once for the entire REPL lifetime.
	sess, err := newAgentSession()
	if err != nil {
		return err
	}
	defer sess.close()

	fmt.Fprintf(os.Stderr, "  %s %s/%s\n", Dim("provider:"), sess.providerName, sess.modelName)

	scanner := bufio.NewScanner(os.Stdin)
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

		if err := repl.AppendHistory(input); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not save history: %v\n", err)
		}

		response, streamed, err := sess.runPrompt(input)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s\n", replYellow("error: "+err.Error()))
		} else if !streamed && response != "" {
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

	sess, err := newAgentSession()
	if err != nil {
		return err
	}
	defer sess.close()

	cmd.Printf("  %s %s/%s\n\n", Dim("provider:"), sess.providerName, sess.modelName)

	response, streamed, err := sess.runPrompt(prompt)
	if err != nil {
		return fmt.Errorf("agent error: %w", err)
	}

	if !streamed {
		cmd.Println(response)
	}
	return nil
}

// agentSession holds the long-lived infrastructure shared across REPL and
// one-shot prompts. Building it once avoids re-opening the database,
// re-spawning MCP servers, and re-discovering skills on every turn.
type agentSession struct {
	cfg          *config.Config
	provider     agent.Provider
	providerName string
	modelName    string
	systemPrompt string
	toolReg      *tools.Registry
	db           *memory.DB
	mcpClients   []mcp.MCPClient
	messages     []types.Message
}

func newAgentSession() (*agentSession, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	provider := resolveProvider(cfg)
	modelName := resolveModel(cfg)
	providerName := resolveProviderName(cfg)

	cwd, _ := os.Getwd()
	instrFiles := instructions.Load(cwd, cwd)
	systemPrompt := "You are yaah, a helpful AI assistant. Respond concisely."
	if formatted := instructions.FormatForSystem(instrFiles); formatted != "" {
		systemPrompt += "\n\n" + formatted
	}

	toolReg := tools.NewRegistry()

	db, err := memory.OpenDefault()
	if err == nil {
		toolReg.Register(&tools.MemorySearchTool{DB: db})
		toolReg.Register(&tools.MemoryAddTool{DB: db})
	}

	mcpDirs := mcpSearchPaths(config.HomeDir())
	mcpClients, mcpTools, mcpErr := mcp.StartMCPClients(context.Background(), mcpDirs)
	if mcpErr != nil {
		fmt.Fprintf(os.Stderr, "warning: MCP startup error: %v\n", mcpErr)
	}
	for _, t := range mcpTools {
		toolReg.Register(t)
	}

	skillDirs := skillSearchPaths()
	toolReg.Register(&tools.SkillTool{Dirs: skillDirs})

	todoStore := todo.NewStore()
	toolReg.Register(&tools.TodoWriteTool{
		Store: todoStore,
		OnWrite: func() {
			if formatted := todoStore.Format(); formatted != "" {
				fmt.Fprintf(os.Stderr, "\n%s\n", formatted)
			}
		},
	})

	return &agentSession{
		cfg:          cfg,
		provider:     provider,
		providerName: providerName,
		modelName:    modelName,
		systemPrompt: systemPrompt,
		toolReg:      toolReg,
		db:           db,
		mcpClients:   mcpClients,
	}, nil
}

func (s *agentSession) close() {
	if s.db != nil {
		s.db.Close()
	}
	for _, c := range s.mcpClients {
		c.Close()
	}
}

// runPrompt executes a single agent prompt with the session's shared
// infrastructure and per-turn callbacks (spinner, streaming display).
func (s *agentSession) runPrompt(prompt string) (string, bool, error) {
	loop := &agent.Loop{
		Provider:      s.provider,
		Registry:      s.toolReg,
		Model:         s.modelName,
		SystemPrompt:  s.systemPrompt,
		MaxIterations: s.cfg.Default.MaxIterations,
		Messages:      s.messages,
	}

	spin := spinner.New(nil, "Thinking...")
	spin.Start()

	streamed := false
	loop.OnToken = func(token string) {
		if !streamed {
			spin.Stop()
			streamed = true
		}
		fmt.Fprint(os.Stderr, token)
	}

	loop.OnTool = func(info agent.ToolInfo) {
		if !streamed {
			spin.Stop()
			streamed = true
		}

		if info.Duration == 0 {
			fmt.Fprintf(os.Stderr, "\n  tool: %s", Bold(info.Name))
			if info.Args != "" {
				args := info.Args
				if len(args) > 60 {
					args = args[:57] + "..."
				}
				fmt.Fprintf(os.Stderr, "(%s)", Dim(args))
			}
		} else {
			fmt.Fprintf(os.Stderr, " (%s)\n", Dim(fmt.Sprintf("%.1fs", info.Duration.Seconds())))
			if info.Error != "" {
				fmt.Fprintf(os.Stderr, "    %s\n", replYellow("error: "+info.Error))
			}
		}
	}

	response, err := loop.Run(context.Background(), prompt)
	if !streamed {
		spin.Stop()
	}

	if streamed {
		fmt.Fprintln(os.Stderr)
	}

	// Persist the conversation history for the next turn.
	s.messages = loop.Messages

	return response, streamed, err
}

// resolveProviderName extracts the provider name from the config.
func resolveProviderName(cfg *config.Config) string {
	modelParts := strings.SplitN(cfg.Default.Model, "/", 2)
	if len(modelParts) == 2 {
		return modelParts[0]
	}
	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > 0 {
		return names[0]
	}
	return "local"
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

	// Deterministic fallback: prefer the first provider by sorted name with a real key.
	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := cfg.Providers[name]
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

func (s *noProviderStub) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
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
