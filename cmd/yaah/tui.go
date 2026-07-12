package yaah

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/instructions"
	"github.com/buchenberg/yaah/internal/mcp"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/todo"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/tui"
	"github.com/buchenberg/yaah/internal/types"
	tea "github.com/charmbracelet/bubbletea"
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
	if err == nil {
		toolReg.Register(&tools.MemorySearchTool{DB: db})
		toolReg.Register(&tools.MemoryAddTool{DB: db})
		defer db.Close()
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
		func(input string) {
			go runAgentForTUI(input, agentCh, cfg, systemPrompt, modelName, toolReg, &messages)
		},
		func() {},
	)

	p := tea.NewProgram(m, tea.WithAltScreen())

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
func runAgentForTUI(prompt string, ch chan<- tui.AgentMsg, cfg *config.Config, systemPrompt, modelName string, toolReg *tools.Registry, messages *[]types.Message) {
	provider := resolveProvider(cfg)

	loop := &agent.Loop{
		Provider:      provider,
		Registry:      toolReg,
		Model:         modelName,
		SystemPrompt:  systemPrompt,
		MaxIterations: cfg.Default.MaxIterations,
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
			}
		},
	}

	response, err := loop.Run(context.Background(), prompt)
	if err != nil {
		ch <- tui.AgentMsg{Err: err}
		*messages = loop.Messages
		return
	}

	*messages = loop.Messages
	ch <- tui.AgentMsg{Done: true, Response: response}
}
