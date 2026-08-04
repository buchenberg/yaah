package yaah

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/buchenberg/yaah/internal/tools"
	"github.com/buchenberg/yaah/internal/tui2"
	"github.com/buchenberg/yaah/internal/types"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(tui2Cmd)
}

var tui2Cmd = &cobra.Command{
	Use:   "tui2",
	Short: "Start the experimental tview-based TUI",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runTUI2()
	},
}

func runTUI2() error {
	// tview's full-screen redraw is expensive on Windows when background
	// subprocesses (MCP servers) are running. Skip MCP + OTel for tui2
	// to keep the UI responsive. The agent loop still works; MCP tools
	// are simply not available in this mode.
	sess, err := newAgentSessionWithOptions(false, false)
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}
	defer sess.close()

	controlCh := make(chan types.CtrlMsg, 64)
	sess.SetCtrlCh(controlCh)

	app := tui2.New()

	app.SetProvider(sess.ProviderName())
	app.SetModel(sess.ModelName())

	// Fetch available models and send as CtrlModelList so the model picker
	// (triggered via ":model") has data to display.
	cfg := sess.cfg
	names := make(map[string]string)
	for key, p := range cfg.Providers {
		if p.Name != "" {
			names[key] = p.Name
		}
	}
	controlCh <- &types.CtrlModelList{Models: fetchAllModels(context.Background(), cfg), ProviderNames: names}

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
		if cancelAgent != nil {
			cancelAgent()
			cancelAgent = nil
			app.HideThinking()
		}
	}
	app.OnCompact = func() {
		go sess.Compact()
	}
	app.OnClear = func() {}

	app.ControlCh = controlCh
	sess.SetView(app)

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
					controlCh <- &types.CtrlQuestion{
						Header:   e.Header,
						Question: e.Question,
						Options:  opts,
						Multiple: e.Multiple,
						AnswerCh: ch,
					}
					answer := <-ch
					answers = append(answers, fmt.Sprintf("%s: %s", e.Header, answer))
				}
				return answers
			}
		}
	}

	origStderr := os.Stderr
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(origStderr, "TUI2 panic: %v\n", r)
		}
		os.Stderr = origStderr
	}()

	return app.Run()
}
