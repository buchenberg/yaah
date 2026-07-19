package yaah

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
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

	fmt.Fprintf(os.Stderr, "\n  %s %s/%s\n\n", Dim("provider:"), sess.providerName, sess.modelName)

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
	procMgr      *processpkg.Manager
	messages     []types.Message
	sessionID    string
	msgIdx       int
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

	// Load sub-agent role definitions: built-in (embedded) +
	// user-defined (~/.agents/roles/, ./.agents/roles/).
	reg := agent.NewRoleRegistry()
	if files := builtinRoleFiles(); files != nil {
		reg.LoadBytes(files)
	}
	for _, dir := range roleSearchPaths(cwd) {
		reg.LoadDir(dir)
	}
	agent.SetDefaultRoleRegistry(reg)

	layers := prompts.Layers{
		Identity:    prompts.IdentityPrompt,
		Environment: prompts.DetectEnvironment(cwd),
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
	mcpClients, mcpTools, _, mcpErr := mcp.StartMCPClientsWithStderr(context.Background(), mcpDirs, io.Discard)
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

	var messages []types.Message
	var sessionID string
	var msgIdx int
	if resumeSessionID != "" {
		if db == nil {
			return nil, fmt.Errorf("cannot resume session: no database available (run 'yaah doctor')")
		}
		dbMsgs, err := db.GetMessages(resumeSessionID)
		if err != nil {
			return nil, fmt.Errorf("cannot resume session %s: %w", resumeSessionID, err)
		}
		if len(dbMsgs) == 0 {
			return nil, fmt.Errorf("session %s not found or has no messages", resumeSessionID)
		}
		messages = make([]types.Message, 0, len(dbMsgs))
		for _, m := range dbMsgs {
			msg := types.Message{
				Role:    m.Role,
				Content: m.Content,
				Name:    m.ToolName,
			}
			if m.ToolCalls != "" {
				json.Unmarshal([]byte(m.ToolCalls), &msg.ToolCalls)
			}
			messages = append(messages, msg)
		}
		if len(messages) > 0 {
			last := messages[len(messages)-1]
			if last.Role == "assistant" && len(last.ToolCalls) > 0 {
				messages = append(messages, types.SystemMsg(
					"Previous execution was interrupted. Please continue from where you left off."))
			}
		}
		sessionID = resumeSessionID
		msgIdx = len(dbMsgs)
	} else {
		sessionID = fmt.Sprintf("sess-%d", time.Now().UnixNano())
		if db != nil {
			cwd, _ := os.Getwd()
			db.CreateSession(memory.Session{
				ID:        sessionID,
				StartedAt: time.Now().Unix(),
				CWD:       cwd,
				Model:     modelName,
			})
		}
	}

	toolReg.Register(newTaskTool(provider, systemPrompt, modelName, db, sessionID, cfg.Agent.SubAgent, reg.Names()))

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
		messages:     messages,
		msgIdx:       msgIdx,
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
		Provider:               s.provider,
		CompactProvider:        compactProvider,
		CompactModel:           compactModel,
		Registry:               s.toolReg,
		Model:                  s.modelName,
		SystemPrompt:           s.systemPrompt,
		MaxIterations:          s.cfg.Default.MaxIterations,
		ContextWindow:          s.cfg.Default.ContextWindow,
		ApprovalMode:           resolveApproval(s.cfg),
		Messages:               s.messages,
		HookDir:                s.cfg.Hooks.Dir,
		SessionID:              s.sessionID,
		PipelineNames:          s.cfg.Agent.Middleware.Enabled,
		PipelineDisabled:       s.cfg.Agent.Middleware.Disabled,
		DB:                     s.db,
		MsgIdx:                 s.msgIdx,
		MaxSubAgentDepth:       s.cfg.Agent.SubAgent.MaxDepth,
		MaxSubAgentConcurrency: s.cfg.Agent.SubAgent.MaxConcurrency,
		MaxSubAgentDepthByRole: subAgentDepthByRole(s.cfg.Agent.SubAgent),
	}

	spin := spinner.New(nil, "Thinking...")
	fmt.Fprintln(os.Stderr)
	spin.Start()

	streamed := false
	loop.OnToken = func(token string) {
		if !streamed {
			spin.Stop()
			fmt.Fprintln(os.Stderr)
			streamed = true
		}
		fmt.Fprint(os.Stderr, token)
	}

	loop.OnTool = func(info agent.ToolInfo) {
		if info.Duration == 0 {
			spin.Stop()
			return
		}
		if info.Name == "task" {
			return // sub-agent lifecycle rendered by OnSubAgent
		}

		fmt.Fprintf(os.Stderr, "\n  tool: %s", Bold(info.Name))
		if info.Args != "" {
			args := info.Args
			if len(args) > 60 {
				args = args[:57] + "..."
			}
			fmt.Fprintf(os.Stderr, "(%s)", Dim(args))
		}
		fmt.Fprintf(os.Stderr, " (%s)\n", Dim(formatDuration(info.Duration)))
		if info.Error != "" {
			fmt.Fprintf(os.Stderr, "    %s\n", replYellow("error: "+info.Error))
		}
	}

	loop.OnSubAgent = func(info agent.SubAgentInfo) {
		if info.Duration == 0 {
			fmt.Fprintf(os.Stderr, "\n  >>> sub-agent: %s — %s\n", Bold(info.Role), info.Prompt)
		} else {
			status := "completed"
			if info.Error != "" {
				status = replYellow(info.Error)
			}
			fmt.Fprintf(os.Stderr, "  <<< sub-agent: %s — %s (%s)\n", Bold(info.Role), status, Dim(formatDuration(info.Duration)))
		}
	}

	response, err := loop.Run(context.Background(), prompt)
	if !streamed {
		spin.Stop()
	}

	if streamed {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr)
	}

	// Persist the conversation history for the next turn.
	s.messages = loop.Messages
	s.msgIdx = loop.MsgIdx

	return response, streamed, err
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

// newTaskTool builds the root-level TaskTool wired to the session's
// provider, prompt, db, and sub-agent config. Shared by the REPL and
// TUI entrypoints so the two cannot drift.
func newTaskTool(provider agent.Provider, systemPrompt, modelName string, db *memory.DB, sessionID string, subCfg config.SubAgentConfig, roleNames []string) *tools.TaskTool {
	// Map a config "0 = unlimited" MaxDepth to a sentinel so the
	// structural nesting decrement in makeTaskRunner does not disable
	// spawning for an "unlimited" setting.
	depth := subCfg.MaxDepth
	if depth <= 0 {
		depth = math.MaxInt32
	}
	return &tools.TaskTool{
		Runner: makeTaskRunner(taskRunnerOpts{
			provider:        provider,
			systemPrompt:    systemPrompt,
			modelName:       modelName,
			db:              db,
			parentSession:   sessionID,
			subCfg:          subCfg,
			SubToolCallback: subToolDisplay,
		}, depth),
		ResolveTimeout: subAgentTimeoutResolver(subCfg),
		RoleNames:      roleNames,
	}
}

// subToolDisplay prints sub-agent tool calls indented under the
// parent's sub-agent banner so they are visually distinct.
func subToolDisplay(info agent.ToolInfo) {
	if info.Duration == 0 {
		return
	}
	fmt.Fprintf(os.Stderr, "    tool: %s", Bold(info.Name))
	if info.Args != "" {
		args := info.Args
		if len(args) > 40 {
			args = args[:37] + "..."
		}
		fmt.Fprintf(os.Stderr, "(%s)", Dim(args))
	}
	fmt.Fprintf(os.Stderr, " (%s)\n", Dim(formatDuration(info.Duration)))
	if info.Error != "" {
		fmt.Fprintf(os.Stderr, "      %s\n", replYellow("error: "+info.Error))
	}
}

// builtinRoleFiles reads the embedded roles/*.md files shipped in the
// binary and returns them keyed by file name (e.g. "worker.md").
func builtinRoleFiles() map[string][]byte {
	entries, err := prompts.BuiltinRolesFS.ReadDir("roles")
	if err != nil {
		return nil
	}
	files := make(map[string][]byte, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, _ := prompts.BuiltinRolesFS.ReadFile("roles/" + e.Name())
		files[e.Name()] = data
	}
	return files
}

// roleSearchPaths returns directories to scan for user-defined role
// definitions. Mirrors the skill search hierarchy: project-level
// (walked up from cwd) then user-level (~/.agents/roles/).
func roleSearchPaths(cwd string) []string {
	home := config.HomeDir()
	var dirs []string
	for dir := cwd; ; dir = filepath.Dir(dir) {
		dirs = append(dirs, filepath.Join(dir, ".agents", "roles"))
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	dirs = append(dirs, filepath.Join(home, ".agents", "roles"))
	return dirs
}

// taskRunnerOpts holds the shared state needed to build sub-agent loops.
// It is captured by every makeTaskRunner closure so nested sub-agents
// (planner → worker) inherit the same provider, prompt base, and config.
type taskRunnerOpts struct {
	provider      agent.Provider
	systemPrompt  string
	modelName     string
	db            *memory.DB
	parentSession string
	subCfg        config.SubAgentConfig

	// SubToolCallback is set on the sub-loop's OnTool so sub-agent
	// tool calls can be rendered indented in the CLI.
	SubToolCallback agent.ToolCallback
}

// subAgentSeq guarantees unique sub-session IDs across concurrent
// goroutines without relying on wall-clock resolution.
var subAgentSeq atomic.Int64

// makeTaskRunner creates a sub-agent runner that honours roles, timeouts,
// iteration caps, and nesting depth.
//
// remainingDepth bounds how many levels of nested task calls the
// returned runner may itself issue. When it reaches zero the task tool
// is omitted from the sub-loop's registry, so nesting is bounded
// structurally without relying on middleware alone. A zero/negative
// config MaxDepth is mapped to a sentinel so "unlimited" is preserved.
func makeTaskRunner(opts taskRunnerOpts, remainingDepth int) tools.TaskRunner {
	return func(ctx context.Context, prompt string, params tools.SubAgentParams) (string, error) {
		role := agent.SubAgentRole(params.Role)
		profile := agent.RoleProfileFor(role)

		subReg := buildSubAgentRegistry(opts, profile, remainingDepth)

		maxIter := resolveSubAgentIterations(params.MaxIterations, profile, opts.subCfg, role)
		sysPrompt := opts.systemPrompt
		if g := agent.RoleGuidance(role); g != "" {
			if sysPrompt != "" {
				sysPrompt += "\n\n"
			}
			sysPrompt += g
		}

		// Persist the sub-agent transcript under a child session. The ID
		// combines wall-clock time with a process-wide atomic counter so
		// parallel task calls cannot collide; if session creation fails
		// the sub-agent runs in-memory rather than polluting the parent
		// transcript.
		subDB := opts.db
		subSessionID := opts.parentSession
		if opts.db != nil {
			subSessionID = fmt.Sprintf("%s-sub-%d-%d", opts.parentSession, time.Now().UnixNano(), subAgentSeq.Add(1))
			cwd, _ := os.Getwd()
			if err := opts.db.CreateSession(memory.Session{
				ID:        subSessionID,
				StartedAt: time.Now().Unix(),
				CWD:       cwd,
				Model:     opts.modelName,
			}); err != nil {
				subDB = nil
			}
		}

		subLoop := &agent.Loop{
			Provider:               opts.provider,
			Registry:               subReg,
			SystemPrompt:           sysPrompt,
			Model:                  opts.modelName,
			MaxIterations:          maxIter,
			MaxRetries:             2,
			ApprovalMode:           "allow",
			DB:                     subDB,
			SessionID:              subSessionID,
			MaxSubAgentDepth:       opts.subCfg.MaxDepth,
			MaxSubAgentConcurrency: opts.subCfg.MaxConcurrency,
			MaxSubAgentDepthByRole: subAgentDepthByRole(opts.subCfg),
			OnTool:                 opts.SubToolCallback,
		}

		return subLoop.Run(ctx, prompt)
	}
}

// subAgentTimeoutResolver returns a TaskTool timeout resolver that folds
// in the actual per-call role, so role-profile and per-role config
// timeouts are honoured rather than a single construction-time default.
func subAgentTimeoutResolver(subCfg config.SubAgentConfig) func(tools.SubAgentParams) time.Duration {
	return func(p tools.SubAgentParams) time.Duration {
		return resolveSubAgentTimeout(0, subCfg, agent.SubAgentRole(p.Role))
	}
}

// subAgentDepthByRole builds the per-role depth cap map from role
// profile defaults, overridden by per-role config. Roles absent from
// the map fall back to the global MaxDepth in the middleware.
func subAgentDepthByRole(subCfg config.SubAgentConfig) map[agent.SubAgentRole]int {
	out := make(map[agent.SubAgentRole]int)
	for _, role := range []agent.SubAgentRole{agent.RoleWorker, agent.RoleReviewer, agent.RolePlanner} {
		if d := agent.RoleProfileFor(role).MaxDepth; d > 0 {
			out[role] = d
		}
	}
	for name, rc := range subCfg.Roles {
		if rc.MaxDepth > 0 {
			out[agent.SubAgentRole(name)] = rc.MaxDepth
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// buildSubAgentRegistry constructs a tool registry for the given role
// profile. If the profile includes the task tool and remainingDepth > 0,
// a nested TaskTool is registered so the sub-agent can spawn further
// workers. When remainingDepth == 0 the task tool is omitted entirely.
//
// The RoleDefault profile (empty Tools) falls back to the full built-in
// tool set to preserve the legacy task tool behaviour.
func buildSubAgentRegistry(opts taskRunnerOpts, profile agent.RoleProfile, remainingDepth int) *tools.Registry {
	resolveTimeout := subAgentTimeoutResolver(opts.subCfg)
	registerTask := func(reg *tools.Registry) {
		reg.Register(&tools.TaskTool{
			Runner:         makeTaskRunner(opts, remainingDepth-1),
			ResolveTimeout: resolveTimeout,
		})
	}

	if len(profile.Tools) == 0 {
		// Legacy default: the full built-in tool set.
		reg := tools.NewRegistry()
		if remainingDepth > 0 {
			registerTask(reg)
		}
		return reg
	}

	reg := tools.NewEmptyRegistry()
	for _, name := range profile.Tools {
		if name == "task" {
			if remainingDepth > 0 {
				registerTask(reg)
			}
			continue
		}
		if t := tools.NewLeafTool(name); t != nil {
			reg.Register(t)
		}
	}
	return reg
}

// resolveSubAgentTimeout picks the effective default timeout for a
// sub-agent TaskTool. Precedence: per-call override (handled by the
// TaskTool itself) > role-specific config > role profile default >
// global subagent default_timeout.
func resolveSubAgentTimeout(callSeconds int, subCfg config.SubAgentConfig, role agent.SubAgentRole) time.Duration {
	if callSeconds > 0 {
		return time.Duration(callSeconds) * time.Second
	}
	if rc, ok := subCfg.Roles[string(role)]; ok && rc.Timeout > 0 {
		return time.Duration(rc.Timeout) * time.Second
	}
	if d := agent.RoleProfileFor(role).Timeout; d > 0 {
		return d
	}
	if subCfg.DefaultTimeout > 0 {
		return time.Duration(subCfg.DefaultTimeout) * time.Second
	}
	return 0
}

// resolveSubAgentIterations picks the iteration cap for a sub-agent Loop.
// Precedence: per-call override > role-specific config > role profile
// default > a sane floor of 1. The result is never allowed to exceed the
// role profile's MaxIterations ceiling, so a per-call override cannot
// neutralize the role's cap.
func resolveSubAgentIterations(callMax int, profile agent.RoleProfile, subCfg config.SubAgentConfig, role agent.SubAgentRole) int {
	var v int
	switch {
	case callMax > 0:
		v = callMax
	case subCfg.Roles[string(role)].MaxIterations > 0:
		v = subCfg.Roles[string(role)].MaxIterations
	case profile.MaxIterations > 0:
		v = profile.MaxIterations
	default:
		return 1
	}
	if profile.MaxIterations > 0 && v > profile.MaxIterations {
		v = profile.MaxIterations
	}
	return v
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

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// printHelp displays the available slash commands.
func printHelp() {
	fmt.Printf("  %s  %s\n", repl.Bold("/exit"), repl.Dim("quit yaah"))
	fmt.Printf("  %s  %s\n", repl.Bold("/clear"), repl.Dim("clear the screen"))
	fmt.Printf("  %s  %s\n", repl.Bold("/compact"), repl.Dim("summarize old messages to free context"))
	fmt.Printf("  %s  %s\n", repl.Bold("/?"), repl.Dim("show this help"))
	fmt.Println()
}
