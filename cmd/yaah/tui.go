package yaah

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"

	tea "charm.land/bubbletea/v2"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/mcp"
	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/tui"
	"github.com/buchenberg/yaah/internal/types"
	zone "github.com/lrstanley/bubblezone/v2"
	"github.com/spf13/cobra"
)

var (
	tuiMCP     bool
	tuiMCPHTTP string
	tuiMCPBuf  *observability.BufferingSpanProcessor
)

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
	tuiCmd.Flags().BoolVar(&tuiMCP, "mcp", false, "expose TUI session as MCP server over stdio")
	tuiCmd.Flags().StringVar(&tuiMCPHTTP, "mcp-http", "", "expose TUI session as MCP server at this HTTP address (e.g. 127.0.0.1:7334)")
	rootCmd.AddCommand(tuiCmd)
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
		log.SetOutput(devNull)
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

	if tuiMCP || tuiMCPHTTP != "" {
		tuiMCPBuf = observability.NewBufferingSpanProcessor()
		extraOtelProcessors = []sdktrace.SpanProcessor{tuiMCPBuf}
		otelInMemoryOnly = true
		defer func() {
			extraOtelProcessors = nil
			otelInMemoryOnly = false
		}()
	}

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

	// Create view once, fill program pointer after tea.NewProgram
	var prog *tea.Program
	fwd := &agentViewFwd{}
	sess.SetView(fwd)

	var cancelAgent context.CancelFunc // accessed only from bubbletea goroutine (OnSubmit/OnAbort) — no mutex needed
	m := tui.New(tui.Config{
		Provider:      sess.ProviderName(),
		Model:         sess.ModelName(),
		CWD:           cwd,
		ContextWindow: providers.ResolveWindow(cfg.Agent.Default.Model, cfg.Agent.Default.ContextWindow),
		Version:       version,
		Verbose:       cfg.TUI.Verbose,
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
		OnLogin: func() {
			go tuiLogin(sess.cfg, prog)
		},
		OnLogout: func() {
			go tuiLogout(sess.cfg, prog)
		},
	})

	// Show MCP server status.
	m.SetMCPInfos(tuiMCPInfos)
	m.RegisterCommand(":mcp", "Show MCP server status")

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
	fwd.program = prog // fill program pointer before first RunPrompt

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

	if tuiMCP || tuiMCPHTTP != "" {
		srv := mcp.NewServer("yaah-tui", version)
		var mu sync.Mutex
		var totalTokens types.Usage
		var promptCount int
		var sessPtr *agentSession = sess
		registerServeTools(srv, &mu, &totalTokens, &promptCount, tuiMCPBuf, &sessPtr, nil, nil)

		if tuiMCPHTTP != "" {
			httpSrv := mcp.NewHTTPServer(srv, tuiMCPHTTP)
			go func() { _ = httpSrv.Start(context.Background()) }()
		} else {
			go func() { _ = srv.Serve(context.Background(), os.Stdin, os.Stdout) }()
		}
	}

	// Pre-fetch model lists from all providers in the background.
	go func() {
		names := make(map[string]string)
		for key, p := range cfg.Providers {
			if p.Name != "" {
				names[key] = p.Name
			}
		}
		models := providers.FetchAllModels(context.Background(), cfg, makeModelLister)
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
