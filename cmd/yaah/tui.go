package yaah

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/instructions"
	"github.com/buchenberg/yaah/internal/mcp"
	"github.com/buchenberg/yaah/internal/memory"
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

	// Shared conversation history for the TUI session.
	var messages []types.Message

	m := tui.New(
		providerName,
		modelName,
		cfg.Default.ContextWindow,
		func(input string) {
			go runAgentForTUI(input, agentCh, cfg, systemPrompt, modelName, toolReg, &messages, db, sessionID, &msgIdx, &persistedCount)
		},
		func() {},
	)

	p := tea.NewProgram(m)

	go func() {
		for msg := range agentCh {
			p.Send(msg)
		}
	}()

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	return nil
}

// runAgentForTUI runs the agent loop for a single prompt and sends messages
// to the TUI channel. The channel is NOT closed here — it is shared across
// multiple prompts for the lifetime of the TUI session.
func runAgentForTUI(prompt string, ch chan<- tui.AgentMsg, cfg *config.Config, systemPrompt, modelName string, toolReg *tools.Registry, messages *[]types.Message, db *memory.DB, sessionID string, msgIdx *int, persistedCount *int) {
	provider := resolveProvider(cfg)

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
