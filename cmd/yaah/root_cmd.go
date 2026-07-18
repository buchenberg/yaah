package yaah

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/instructions"
	"github.com/buchenberg/yaah/internal/mcp"
	"github.com/buchenberg/yaah/internal/memory"
	processpkg "github.com/buchenberg/yaah/internal/process"
	"github.com/buchenberg/yaah/internal/prompts"
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
		case repl.CmdCompact:
			sess.compactContext()
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
	cfg            *config.Config
	provider       agent.Provider
	providerName   string
	modelName      string
	systemPrompt   string
	toolReg        *tools.Registry
	db             *memory.DB
	mcpClients     []mcp.MCPClient
	procMgr        *processpkg.Manager
	messages       []types.Message
	sessionID      string
	msgIdx         int
	persistedCount int
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

	layers := prompts.Layers{
		Identity:    prompts.IdentityPrompt,
		Environment: prompts.DetectEnvironment(),
		UserContext: prompts.LoadUserContext(config.HomeDir()),
		Project:     instructions.FormatForSystem(instructions.Load(cwd, cwd)),
	}

	toolReg := tools.NewRegistry()

	db, err := memory.OpenDefault()
	if err == nil {
		if entries, memErr := db.ListMemory(50); memErr == nil && len(entries) > 0 {
			var memLines []string
			for _, entry := range entries {
				memLines = append(memLines, "- "+entry.Text)
			}
			layers.Memory = "You have the following stored information about the user and project:\n" + strings.Join(memLines, "\n")
		}

		toolReg.Register(&tools.MemorySearchTool{DB: db})
		toolReg.Register(&tools.MemoryAddTool{DB: db})
		toolReg.Register(&tools.MemoryDeleteTool{DB: db})
		toolReg.Register(&tools.MemoryUpdateTool{DB: db})
		toolReg.Register(&tools.MemorySessionSearchTool{DB: db})
	}

	systemPrompt := prompts.Build(layers)

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

	var todoStore *todo.Store
	if db != nil {
		todoStore = todo.NewStoreWithDB(db)
		if err := todoStore.LoadFromDB(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not load todos: %v\n", err)
		}
	} else {
		todoStore = todo.NewStore()
	}
	toolReg.Register(&tools.TodoWriteTool{
		Store: todoStore,
		OnWrite: func() {
			if formatted := todoStore.Format(); formatted != "" {
				fmt.Fprintf(os.Stderr, "\n%s\n", formatted)
			}
		},
	})

	procMgr := processpkg.NewManager()
	toolReg.Register(&tools.BackgroundProcessTool{Manager: procMgr})

	toolReg.Register(&tools.TaskTool{
		Runner: makeTaskRunner(provider, systemPrompt, modelName),
	})

	sessionID := fmt.Sprintf("sess-%d", time.Now().UnixNano())
	if db != nil {
		cwd, _ := os.Getwd()
		db.CreateSession(memory.Session{
			ID:        sessionID,
			StartedAt: time.Now().Unix(),
			CWD:       cwd,
			Model:     modelName,
		})
	}

	return &agentSession{
		cfg:          cfg,
		provider:     provider,
		providerName: providerName,
		modelName:    modelName,
		systemPrompt: systemPrompt,
		toolReg:      toolReg,
		db:           db,
		mcpClients:   mcpClients,
		procMgr:      procMgr,
		sessionID:    sessionID,
	}, nil
}

func (s *agentSession) close() {
	if s.db != nil {
		s.db.EndSession(s.sessionID, time.Now().Unix())
		s.db.Close()
	}
	for _, c := range s.mcpClients {
		c.Close()
	}
}

func (s *agentSession) compactContext() {
	if len(s.messages) <= 4 {
		fmt.Fprintf(os.Stderr, "  %s\n", Dim("context is already small enough"))
		return
	}

	window := s.cfg.Default.ContextWindow
	if window <= 0 {
		window = 128000
	}

	totalChars := 0
	for _, m := range s.messages {
		totalChars += len(m.Content)
	}
	estTokens := totalChars / 4
	target := window * 4 / 5

	if estTokens <= target {
		fmt.Fprintf(os.Stderr, "  %s %d/%d tokens (%d%%)\n",
			Dim("context:"), estTokens, window, estTokens*100/window)
		return
	}

	fmt.Fprintf(os.Stderr, "  %s %d/%d tokens (%d%%) — compacting...\n",
		Dim("context:"), estTokens, window, estTokens*100/window)

	sysMsg := s.messages[0]
	rest := s.messages[1:]

	split := len(rest) / 2
	oldMsgs := rest[:split]
	keepMsgs := rest[split:]

	var sb strings.Builder
	sb.WriteString("Summarize the following conversation excerpt in 2-3 sentences. Be concise and factual.\n\n")
	for _, m := range oldMsgs {
		if m.Content != "" {
			sb.WriteString(fmt.Sprintf("%s: %s\n", m.Role, m.Content))
		}
	}

	req := types.ChatRequest{
		Model: s.modelName,
		Messages: []types.Message{
			types.UserMsg(sb.String()),
		},
	}

	resp, err := s.provider.Send(context.Background(), req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  %s\n", replYellow("compact failed: "+err.Error()))
		return
	}

	if len(resp.Choices) == 0 {
		fmt.Fprintf(os.Stderr, "  %s\n", replYellow("compact failed: no response"))
		return
	}

	summary := resp.Choices[0].Message.Content
	newMsgs := []types.Message{sysMsg}
	newMsgs = append(newMsgs, types.SystemMsg("Previous conversation summary: "+summary))
	newMsgs = append(newMsgs, keepMsgs...)
	s.messages = newMsgs

	newTokens := 0
	for _, m := range s.messages {
		newTokens += len(m.Content) / 4
	}
	fmt.Fprintf(os.Stderr, "  %s %d/%d tokens (%d%%)\n",
		Dim("compacted:"), newTokens, window, newTokens*100/window)
}

// runPrompt executes a single agent prompt with the session's shared
// infrastructure and per-turn callbacks (spinner, streaming display).
func (s *agentSession) runPrompt(prompt string) (string, bool, error) {
	compactProvider, compactModel := resolveCompact(s.cfg)

	loop := &agent.Loop{
		Provider:        s.provider,
		CompactProvider: compactProvider,
		CompactModel:    compactModel,
		Registry:        s.toolReg,
		Model:           s.modelName,
		SystemPrompt:    s.systemPrompt,
		MaxIterations:   s.cfg.Default.MaxIterations,
		ContextWindow:   s.cfg.Default.ContextWindow,
		ApprovalMode:    resolveApproval(s.cfg),
		Messages:        s.messages,
		HookDir:         s.cfg.Hooks.Dir,
		SessionID:       s.sessionID,
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

	if s.db != nil {
		s.persistMessages(loop.Messages)
	}

	return response, streamed, err
}

func (s *agentSession) persistMessages(messages []types.Message) {
	newMsgs := messages[s.persistedCount:]
	for _, m := range newMsgs {
		content := m.Content
		if content == "" {
			var parts []string
			for _, tc := range m.ToolCalls {
				parts = append(parts, fmt.Sprintf("[tool:%s] %s", tc.Function.Name, tc.Function.Arguments))
			}
			content = strings.Join(parts, "\n")
		}
		toolCallsJSON := ""
		if len(m.ToolCalls) > 0 {
			data, _ := json.Marshal(m.ToolCalls)
			toolCallsJSON = string(data)
		}
		toolName := ""
		if m.Role == "tool" {
			toolName = m.Name
		}
		msg := memory.Message{
			SessionID: s.sessionID,
			Idx:       s.msgIdx,
			Role:      m.Role,
			Content:   content,
			ToolName:  toolName,
			ToolCalls: toolCallsJSON,
			Timestamp: time.Now().Unix(),
		}
		s.db.AddMessage(msg)
		s.msgIdx++
	}
	s.persistedCount = len(messages)
}

// resolveApproval returns the effective approval mode.
// Order: CLI --approval flag → YAAH_APPROVAL env var → config file → "ask" default.
func resolveApproval(cfg *config.Config) string {
	mode := cfg.Default.Approval
	if v := os.Getenv("YAAH_APPROVAL"); v != "" {
		mode = v
	}
	if approvalOverride != "" {
		mode = approvalOverride
	}
	switch mode {
	case "allow", "ask", "deny":
		return mode
	default:
		fmt.Fprintf(os.Stderr, "warning: unknown approval mode %q, defaulting to 'ask'\n", mode)
		return "ask"
	}
}

// resolveProviderName extracts the provider name from the config.
func resolveProviderName(cfg *config.Config) string {
	// 1. Explicit default.provider setting
	if cfg.Default.Provider != "" {
		if _, ok := cfg.Providers[cfg.Default.Provider]; ok {
			return cfg.Default.Provider
		}
	}
	// 2. Provider/model prefix in default.model
	if parts := strings.SplitN(cfg.Default.Model, "/", 2); len(parts) == 2 {
		if _, ok := cfg.Providers[parts[0]]; ok {
			return parts[0]
		}
	}
	// 3. First provider alphabetically
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

// resolveCompact returns the provider and model to use for context compaction.
// Uses the configured small_model (no tools, fast summarization) if available,
// otherwise falls back to the main provider and model.
func resolveCompact(cfg *config.Config) (agent.Provider, string) {
	if cfg.Default.SmallModel != "" {
		compactProviderName, compactModel := "", ""
		if parts := strings.SplitN(cfg.Default.SmallModel, "/", 2); len(parts) == 2 {
			compactProviderName = parts[0]
			compactModel = parts[1]
		} else {
			compactModel = cfg.Default.SmallModel
			compactProviderName = resolveProviderName(cfg)
		}
		if compactProviderName != "" {
			if p, ok := cfg.Providers[compactProviderName]; ok && isRealKey(p.APIKey) {
				return providers.NewOpenAIClient(p.BaseURL, p.APIKey), compactModel
			}
		}
	}
	return nil, ""
}

// resolveProvider picks the best available provider from the config.
func resolveProvider(cfg *config.Config) agent.Provider {
	providerName := resolveProviderName(cfg)
	if p, ok := cfg.Providers[providerName]; ok && isRealKey(p.APIKey) {
		return providers.NewOpenAIClient(p.BaseURL, p.APIKey)
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

// makeTaskRunner creates a sub-agent runner for the task tool.
func makeTaskRunner(provider agent.Provider, systemPrompt, modelName string) tools.TaskRunner {
	return func(ctx context.Context, prompt string) (string, error) {
		subReg := tools.NewRegistry()
		subReg.Register(&tools.ReadTool{})
		subReg.Register(&tools.WriteTool{})
		subReg.Register(&tools.EditTool{})
		subReg.Register(&tools.DeleteTool{})
		subReg.Register(&tools.GrepTool{})
		subReg.Register(&tools.GlobTool{})
		subReg.Register(&tools.LsTool{})
		subReg.Register(&tools.BashTool{})
		subReg.Register(&tools.PowerShellTool{})
		subReg.Register(&tools.WebFetchTool{})

		subLoop := &agent.Loop{
			Provider:      provider,
			Registry:      subReg,
			SystemPrompt:  systemPrompt,
			Model:         modelName,
			MaxIterations: 25,
			MaxRetries:    2,
			ApprovalMode:  "allow",
		}

		return subLoop.Run(ctx, prompt)
	}
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
	fmt.Printf("  %s  %s\n", repl.Bold("/compact"), repl.Dim("summarize old messages to free context"))
	fmt.Printf("  %s  %s\n", repl.Bold("/?"), repl.Dim("show this help"))
	fmt.Println()
}
