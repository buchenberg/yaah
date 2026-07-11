package yaah

import (
	"context"
	"fmt"
	"os"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/instructions"
	"github.com/buchenberg/yaah/internal/mcp"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/tui"
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
	cfg, _ := config.Load()
	providerName := "unknown"
	modelName := "unknown"
	if cfg != nil {
		providerName = resolveProviderName(cfg)
		modelName = resolveModel(cfg)
	}

	agentCh := make(chan tui.AgentMsg, 64)

	var m *tui.Model
	m = tui.New(
		providerName,
		modelName,
		func(input string) {
			go runAgentForTUI(input, agentCh)
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

// runAgentForTUI runs the agent loop and sends messages to the TUI channel.
func runAgentForTUI(prompt string, ch chan<- tui.AgentMsg) {
	defer close(ch)

	cfg, err := config.Load()
	if err != nil {
		ch <- tui.AgentMsg{Err: fmt.Errorf("config: %w", err)}
		return
	}

	provider := resolveProvider(cfg)
	modelName := resolveModel(cfg)

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
		defer db.Close()
	}

	mcpDirs := mcpSearchPaths(config.HomeDir())
	mcpClients, mcpTools, _ := mcp.StartMCPClients(context.Background(), mcpDirs)
	for _, c := range mcpClients {
		defer c.Close()
	}
	for _, t := range mcpTools {
		toolReg.Register(t)
	}

	loop := &agent.Loop{
		Provider:      provider,
		Registry:      toolReg,
		Model:         modelName,
		SystemPrompt:  systemPrompt,
		MaxIterations: cfg.Default.MaxIterations,
		OnToken: func(token string) {
			ch <- tui.AgentMsg{Token: token}
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
		return
	}

	ch <- tui.AgentMsg{Done: true, Response: response}
}
