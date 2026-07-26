package yaah

import (
	"context"
	"fmt"
	"log"
	"os"
	"sort"

	tea "charm.land/bubbletea/v2"
	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/tui"
	"github.com/buchenberg/yaah/internal/types"
	zone "github.com/lrstanley/bubblezone/v2"
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

		client := providers.NewOpenAIClient(p.BaseURL, p.APIKey, p.TimeoutSeconds)
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

// runTUI starts the bubbletea TUI.
func runTUI() error {
	// Suppress stderr globally while the TUI is active. Anything written to
	// stderr (MCP warnings, tool prompts, etc.) would bleed through the
	// alt-screen and break the layout. We restore stderr on exit.
	origStderr := os.Stderr
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err == nil {
		os.Stderr = devNull
	}
	defer func() {
		os.Stderr = origStderr
		if devNull != nil {
			devNull.Close()
		}
	}()

	zone.NewGlobal()

	// Detect and apply the theme (respects NO_COLOR, YAah_THEME env var,
	// and terminal background).
	tui.ApplyTheme(tui.DetectTheme())

	sess, err := newAgentSession()
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}
	defer sess.close()

	cfg := sess.cfg
	cwd, _ := os.Getwd()

	controlCh := make(chan types.CtrlMsg, 64)
	sess.SetCtrlCh(controlCh)

	tuiMCPInfos := sess.MCPInfos()

	var prog *tea.Program
	var cancelAgent context.CancelFunc // accessed only from bubbletea goroutine (OnSubmit/OnAbort) — no mutex needed
	m := tui.New(tui.Config{
		Provider:      sess.ProviderName(),
		Model:         sess.ModelName(),
		CWD:           cwd,
		ContextWindow: cfg.Agent.Default.ContextWindow,
		OnSubmit: func(input string) {
			ctx, cancel := context.WithCancel(context.Background())
			cancelAgent = cancel
			go func() {
				defer func() {
					if r := recover(); r != nil {
						fmt.Fprintf(os.Stderr, "agent panic: %v\n", r)
						if prog != nil {
							prog.Kill()
						}
					}
				}()
				fwd := &agentViewFwd{program: prog}
				sess.SetView(fwd)
				sess.RunPrompt(ctx, input)
			}()
		},
		OnFollowUp: func(text string) {
			sess.FollowUp(text)
		},
		OnSteer: func(text string) {
			sess.Steer(text)
		},
		OnQuit: func() {},
		OnAbort: func() {
			if cancelAgent != nil {
				cancelAgent()
				cancelAgent = nil
			}
		},
		OnCompact: func() {
			go sess.Compact()
		},
		OnModel: func(pName, mName string) {
			sess.SetModel(pName, mName)
		},
	})

	// Show MCP server status.
	m.SetMCPInfos(tuiMCPInfos)
	m.RegisterCommand(":mcp", "Show MCP server status")
	// Add an initial system message showing MCP status.
	if len(tuiMCPInfos) > 0 {
		var connected, failed int
		for _, info := range tuiMCPInfos {
			if info.Connected {
				connected++
			} else {
				failed++
			}
		}
		statusMsg := fmt.Sprintf("MCP: %d server", len(tuiMCPInfos))
		if len(tuiMCPInfos) > 1 {
			statusMsg += "s"
		}
		if connected > 0 {
			statusMsg += fmt.Sprintf(" (%d connected", connected)
			if failed > 0 {
				statusMsg += fmt.Sprintf(", %d failed", failed)
			}
			statusMsg += ")"
		} else {
			statusMsg += " — all failed"
		}
		statusMsg += ". Type :mcp for details."
		m.AddMessage("system", statusMsg)
	}

	// Panic recovery: catch panics in the main goroutine. When the TUI is
	// running, trigger bubbletea's cleanup to restore the terminal (disable
	// mouse reporting, raw mode, etc.). If the program hasn't started yet,
	// there's nothing to clean up.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(origStderr, "yaah panic: %v\n", r)
			if prog != nil {
				prog.Kill()
			}
			os.Exit(1)
		}
	}()

	// Install suspend/resume signal handlers (no-op on Windows).
	stopSignals := installSignalHandlers()
	defer stopSignals()

	prog = tea.NewProgram(m)

	// Wire the question tool handler for TUI modal dialogs.
	if qt := sess.toolReg.Get("question"); qt != nil {
		if qtp, ok := qt.(*tools.QuestionTool); ok {
			qtp.Handler = func(entries []tools.QuestionEntry) []string {
				var answers []string
				for _, e := range entries {
					ch := make(chan string, 1)
					opts := make([]types.CtrlOption, len(e.Options))
					for i, o := range e.Options {
						opts[i] = types.CtrlOption{Label: o.Label, Description: o.Description}
					}
					prog.Send(&types.CtrlQuestion{
						Header:   e.Header,
						Question: e.Question,
						Options:  opts,
						Multiple: e.Multiple,
						AnswerCh: ch,
					})
					answer := <-ch
					answers = append(answers, fmt.Sprintf("%s: %s", e.Header, answer))
				}
				return answers
			}
		}
	}

	go func() {
		for msg := range controlCh {
			prog.Send(msg)
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
		controlCh <- &types.CtrlModelList{Models: models, ProviderNames: names}
	}()

	if _, err := prog.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	return nil
}

// agentViewFwd implements agent.View and forwards events to a
// bubbletea Program via Send, which is goroutine-safe.
type agentViewFwd struct {
	program *tea.Program
}

func (f *agentViewFwd) HandleEvent(evt agent.Event) {
	f.program.Send(evt)
}
