package yaah

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/instructions"
	"github.com/buchenberg/yaah/internal/mcp"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/todo"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/tui"
	"github.com/buchenberg/yaah/internal/types"
	"github.com/spf13/cobra"
)

// tuiCmd launches the TUI interface.
var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Launch the terminal UI",
	Long:  `Launch the interactive terminal UI with rich chat display, streaming, and tool call visualization.`,
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTUI()
	},
}

func init() {
	rootCmd.AddCommand(tuiCmd)
}

// sessionModel holds the current provider and model for the TUI session.
// It is updated when the user switches models via /model.
type sessionModel struct {
	mu       sync.Mutex
	provider string
	model    string
}

func (s *sessionModel) get() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.provider, s.model
}

func (s *sessionModel) set(provider, model string) {
	s.mu.Lock()
	s.provider = provider
	s.model = model
	s.mu.Unlock()
}

// fetchAllModels gathers model IDs from all configured providers.
// If a provider has a models: override in config, those are used.
// Otherwise, ListModels is called against the provider's /v1/models endpoint.
// Results are returned in "provider/model" format, sorted.
func fetchAllModels(ctx context.Context, cfg *config.Config) []string {
	var all []string

	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		p := cfg.Providers[name]

		if len(p.Models) > 0 {
			for _, m := range p.Models {
				all = append(all, name+"/"+m)
			}
			continue
		}

		client := providers.NewOpenAIClient(p.BaseURL, p.APIKey)
		models, err := client.ListModels(ctx)
		if err != nil {
			log.Printf("fetch models from %s: %v", name, err)
			continue
		}
		for _, m := range models {
			all = append(all, name+"/"+m)
		}
	}

	return all
}

// providerFor returns a provider client for the given provider name.
func providerFor(cfg *config.Config, name string) agent.Provider {
	if p, ok := cfg.Providers[name]; ok && isRealKey(p.APIKey) {
		return providers.NewOpenAIClient(p.BaseURL, p.APIKey)
	}
	return &noProviderStub{}
}

// runTUI starts the bubbletea TUI.
func runTUI() error {
	cfg, err := config.Load()
	if err != nil || cfg == nil {
		cfg = &config.Config{
			Default: config.Defaults{
				Model:         "openai/gpt-4o-mini",
				SmallModel:    "openai/gpt-4o-mini",
				MaxIterations: 50,
			},
		}
	}
	providerName := "unknown"
	modelName := "unknown"
	if cfg != nil {
		providerName = resolveProviderName(cfg)
		modelName = resolveModel(cfg)
	}

	// --- One-time setup: load instructions, memory, MCP tools ---
	cwd, _ := os.Getwd()
	instrFiles := instructions.Load(cwd, cwd)
	systemPrompt := "You are yaah, a helpful AI assistant. Respond concisely."
	if formatted := instructions.FormatForSystem(instrFiles); formatted != "" {
		systemPrompt += "\n\n" + formatted
	}

	toolReg := tools.NewRegistry()

	// Todo store for the session
	todoStore := todo.NewStore()
	toolReg.Register(&tools.TodoWriteTool{Store: todoStore})

	db, err := memory.OpenDefault()
	var sessionID string
	var msgIdx int
	var persistedCount int
	if err == nil {
		if entries, memErr := db.ListMemory(50); memErr == nil && len(entries) > 0 {
			var memLines []string
			for _, entry := range entries {
				memLines = append(memLines, "- "+entry.Text)
			}
			systemPrompt += "\n\n## Memory\nYou have the following stored information about the user and project:\n" + strings.Join(memLines, "\n")
		}
		systemPrompt += "\n\n## Memory Guidelines\n- Use memory_search to find relevant memories before answering personal/project questions. Pass a tag to filter by category.\n- When the user asks about past conversations or session history, use memory_search_sessions with an empty query to list recent transcripts.\n- Use memory_add to save important facts. Always include a tags array (e.g., [\"user_info\"], [\"preferences\"], [\"project:yaah\"], [\"decision\"]).\n- Use memory_update to correct stale facts (requires the memory ID). Use memory_delete to remove incorrect memories.\n- At the end of a conversation or when the user says goodbye, use memory_add to save a 2-3 line summary of key discussion points with tag [\"session_summary\"]."

		toolReg.Register(&tools.MemorySearchTool{DB: db})
		toolReg.Register(&tools.MemoryAddTool{DB: db})
		toolReg.Register(&tools.MemoryDeleteTool{DB: db})
		toolReg.Register(&tools.MemoryUpdateTool{DB: db})
		toolReg.Register(&tools.MemorySessionSearchTool{DB: db})

		cwd, _ := os.Getwd()
		sessionID = fmt.Sprintf("sess-%d", time.Now().UnixNano())
		db.CreateSession(memory.Session{
			ID:        sessionID,
			StartedAt: time.Now().Unix(),
			CWD:       cwd,
			Model:     modelName,
		})

		defer func() {
			db.EndSession(sessionID, time.Now().Unix())
			db.Close()
		}()
	}

	// Start MCP clients once for the entire TUI session.
	mcpDirs := mcpSearchPaths(config.HomeDir())
	mcpClients, mcpTools, _ := mcp.StartMCPClientsWithStderr(context.Background(), mcpDirs, io.Discard)
	for _, c := range mcpClients {
		defer c.Close()
	}
	for _, t := range mcpTools {
		toolReg.Register(t)
	}

	// Register the skill-loading tool
	skillDirs := skillSearchPaths()
	toolReg.Register(&tools.SkillTool{Dirs: skillDirs})

	agentCh := make(chan tui.AgentMsg, 256)

	// Wire the question tool handler for the TUI once at startup.
	if qt, ok := toolReg.Get("question").(*tools.QuestionTool); ok {
		qt.Handler = func(entries []tools.QuestionEntry) []string {
			respCh := make(chan string, 1)
			agentCh <- tui.AgentMsg{Question: entries, QuestionCh: respCh}
			answer := <-respCh
			return strings.Split(answer, "\n")
		}
	}

	// Shared conversation history for the TUI session.
	var messages []types.Message

	// Shared mutable state for the current provider/model.
	sm := &sessionModel{provider: providerName, model: resolveModel(cfg)}

	m := tui.New(
		providerName,
		modelName,
		cfg.Default.ContextWindow,
		func(input string) {
			pName, mName := sm.get()
			go runAgentForTUI(input, agentCh, cfg, systemPrompt, mName, toolReg, &messages, db, sessionID, &msgIdx, &persistedCount, sm)
			_ = pName
		},
		func() {},
		nil,
		func(pName, mName string) {
			sm.set(pName, mName)
		},
	)

	p := tea.NewProgram(m)

	go func() {
		for msg := range agentCh {
			p.Send(msg)
		}
	}()

	// Pre-fetch model lists from all providers in the background.
	go func() {
		names := make(map[string]string)
		for key, p := range cfg.Providers {
			if p.Name != "" {
				names[key] = p.Name
			}
		}
		models := fetchAllModels(context.Background(), cfg)
		agentCh <- tui.AgentMsg{ModelList: models, ProviderNames: names}
	}()

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	return nil
}

// runAgentForTUI runs the agent loop for a single prompt and sends messages
// to the TUI channel. The channel is NOT closed here — it is shared across
// multiple prompts for the lifetime of the TUI session.
func runAgentForTUI(prompt string, ch chan<- tui.AgentMsg, cfg *config.Config, systemPrompt, modelName string, toolReg *tools.Registry, messages *[]types.Message, db *memory.DB, sessionID string, msgIdx *int, persistedCount *int, sm *sessionModel) {
	pName, _ := sm.get()
	provider := providerFor(cfg, pName)

	loop := &agent.Loop{
		Provider:      provider,
		Registry:      toolReg,
		Model:         modelName,
		SystemPrompt:  systemPrompt,
		MaxIterations: cfg.Default.MaxIterations,
		ContextWindow: cfg.Default.ContextWindow,
		Messages:      *messages,
		OnToken: func(token string) {
			ch <- tui.AgentMsg{Token: token}
		},
		OnFlush: func(content string) {
			ch <- tui.AgentMsg{Flush: content}
		},
		OnTool: func(info agent.ToolInfo) {
			if info.Duration == 0 {
				ch <- tui.AgentMsg{ToolName: info.Name}
			} else {
				ch <- tui.AgentMsg{
					ToolResult:     info.Result,
					ToolResultName: info.Name,
				}
			}
		},
		OnThinking: func(text string) {
			ch <- tui.AgentMsg{Thinking: text}
		},
	}

	response, err := loop.Run(context.Background(), prompt)
	if err != nil {
		ch <- tui.AgentMsg{
			Err:           err,
			ContextTokens: loop.EstimatedTokens(),
			ContextWindow: loop.ContextWindow,
		}
		*messages = loop.Messages
		return
	}

	*messages = loop.Messages
	persistTUIMessages(db, sessionID, msgIdx, persistedCount, loop.Messages)
	ch <- tui.AgentMsg{
		Done:          true,
		Response:      response,
		ContextTokens: loop.EstimatedTokens(),
		ContextWindow: loop.ContextWindow,
	}
}

func persistTUIMessages(db *memory.DB, sessionID string, msgIdx *int, persistedCount *int, messages []types.Message) {
	if db == nil {
		return
	}
	newMsgs := messages[*persistedCount:]
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
			SessionID: sessionID,
			Idx:       *msgIdx,
			Role:      m.Role,
			Content:   content,
			ToolName:  toolName,
			ToolCalls: toolCallsJSON,
			Timestamp: time.Now().Unix(),
		}
		db.AddMessage(msg)
		*msgIdx++
	}
	*persistedCount = len(messages)
}
