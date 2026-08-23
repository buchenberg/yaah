package yaah

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/control"
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/tui"
	"github.com/spf13/cobra"
)

// Removed with the bubbletea TUI: the TUI session is no longer exposed as
// an MCP server. The flags remain (hidden) as deprecation stubs so scripts
// get a migration hint instead of "unknown flag".
var (
	tuiMCPDeprecated     bool
	tuiMCPHTTPDeprecated string
)

func init() {
	rootCmd.AddCommand(tuiCmd)
	tuiCmd.Flags().BoolVar(&tuiMCPDeprecated, "mcp", false, "removed: use `yaah serve` for MCP over stdio")
	tuiCmd.Flags().StringVar(&tuiMCPHTTPDeprecated, "mcp-http", "", "removed: use `yaah serve --http <addr>`")
	_ = tuiCmd.Flags().MarkHidden("mcp")
	_ = tuiCmd.Flags().MarkHidden("mcp-http")
}

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Launch the terminal UI",
	Long:  `Launch the interactive terminal UI with rich chat display, streaming, and tool call visualization.`,
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if tuiMCPDeprecated {
			return fmt.Errorf("tui --mcp was removed; use `yaah serve` for MCP over stdio")
		}
		if tuiMCPHTTPDeprecated != "" {
			return fmt.Errorf("tui --mcp-http was removed; use `yaah serve --http %s`", tuiMCPHTTPDeprecated)
		}
		return runTUI()
	},
}

func runTUI() error {
	sess, err := newAgentSession()
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}
	defer sess.close()

	controlCh := make(chan control.Msg, 64)
	sess.SetCtrlCh(controlCh)

	app := tui.New(version)

	app.SetProvider(sess.ProviderName())
	app.SetModel(sess.ModelName())

	cfg := sess.cfg
	subModel := cfg.Agent.SubAgent.Model
	if subModel == "" {
		subModel = cfg.Agent.Default.Model
	}
	subProvider := cfg.Agent.SubAgent.Provider
	if subProvider == "" {
		subProvider = sess.ProviderName()
	}
	mc := cfg.Agent.Middleware
	var pipeline []string
	if len(mc.Enabled) > 0 {
		pipeline = mc.Enabled
	} else {
		defaults := []string{"steer", "followup", "compaction", "soft_prune", "approval", "tool_concurrency", "loop_detection", "staleness"}
		disabled := make(map[string]bool, len(mc.Disabled))
		for _, d := range mc.Disabled {
			disabled[d] = true
		}
		for _, name := range defaults {
			if !disabled[name] {
				pipeline = append(pipeline, name)
			}
		}
	}
	app.SetConfig(
		cfg.Agent.SubAgent.Provider != "" || cfg.Agent.SubAgent.Model != "",
		subProvider,
		cfg.Agent.SubAgent.MaxConcurrency,
		subModel,
		cfg.Embedding.Provider != "" && cfg.Embedding.Model != "",
		cfg.Embedding.Model,
		pipeline,
	)

	names := make(map[string]string)
	for key, p := range cfg.Providers {
		if p.Name != "" {
			names[key] = p.Name
		}
	}
	// Fetch model lists in the background so a slow or unreachable
	// provider endpoint never delays TUI launch; the control channel is
	// buffered and the control loop starts inside app.Run().
	go func() {
		controlCh <- &control.ModelList{Models: providers.FetchAllModels(context.Background(), cfg, makeModelLister), ProviderNames: names}
	}()

	app.OnModelSelect = func(model string) {
		parts := strings.SplitN(model, "/", 2)
		var providerName, modelName string
		if len(parts) == 2 {
			providerName = parts[0]
			modelName = parts[1]
		} else {
			providerName = parts[0]
		}
		sess.SetModel(providerName, modelName)
		app.SetProvider(providerName)
		app.SetModel(modelName)
	}

	var cancelAgent context.CancelFunc
	app.OnSubmit = func(input string) {
		ctx, cancel := context.WithCancel(context.Background())
		cancelAgent = cancel
		app.AddUserMessage(input)
		app.ShowThinking()
		go sess.RunPrompt(ctx, input)
	}
	app.OnAbort = func() {
		app.HideThinking()
		if cancelAgent != nil {
			cancelAgent()
			cancelAgent = nil
		}
	}
	app.OnCompact = func() {
		go sess.Compact()
	}
	app.OnClear = func() {}
	app.OnSteer = func(text string) {
		sess.Steer(text)
	}
	app.OnFollowUp = func(text string) {
		sess.FollowUp(text)
	}
	app.OnStop = func() {
		app.HideThinking()
		if cancelAgent != nil {
			cancelAgent()
			cancelAgent = nil
		}
	}

	app.ControlCh = controlCh
	sess.SetView(app)

	if qt := sess.toolReg.Get("question"); qt != nil {
		if qtp, ok := qt.(*tools.QuestionTool); ok {
			qtp.Handler = func(entries []tools.QuestionEntry) []string {
				var answers []string
				for _, e := range entries {
					ch := make(chan string, 1)
					select {
					case controlCh <- buildCtrlQuestion(e, ch):
					case <-time.After(ctrlSendTimeout):
						answers = append(answers, fallbackCtrlAnswer(e))
						continue
					}
					answers = append(answers, awaitCtrlAnswer(e, ch))
				}
				return answers
			}
		}
	}

	sess.SetApproveFn(func(name, args string) bool {
		ch := make(chan bool, 1)
		select {
		case controlCh <- &control.Approval{
			Name:      name,
			Args:      args,
			ApproveCh: ch,
		}:
		default:
			return false
		}
		select {
		case approved := <-ch:
			return approved
		case <-time.After(30 * time.Second):
			return false
		}
	})

	// Suppress stderr while the TUI is on the alt screen. Anything written
	// to stderr (slow-refresh logs, MCP warnings, tool output) would bleed
	// through and corrupt the layout.
	origStderr := os.Stderr
	devNull, devNullErr := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if devNullErr == nil {
		os.Stderr = devNull
		log.SetOutput(devNull)
	}
	defer func() {
		os.Stderr = origStderr
		log.SetOutput(origStderr)
		if devNull != nil {
			devNull.Close()
		}
	}()
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(origStderr, "TUI panic: %v\n", r)
		}
	}()

	return app.Run()
}
