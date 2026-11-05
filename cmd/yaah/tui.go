package yaah

import (
	"context"
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/control"
	"github.com/buchenberg/yaah/internal/tui"
	"github.com/buchenberg/yaah/internal/tui/components/mcpinfo"
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
	// Interactive surface: honor bare --resume before building the
	// session (stderr is hijacked later; this runs on the real terminal).
	if err := maybeResolveResumePicker(os.Stdout, os.Stdin); err != nil {
		return fmt.Errorf("resume: %v", err)
	}
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
	app.SetMode(sess.Mode())

	infos := sess.MCPInfos()
	mcpServers := make([]mcpinfo.Server, 0, len(infos))
	for _, info := range infos {
		mcpServers = append(mcpServers, mcpinfo.Server{
			Name:      info.Name,
			Connected: info.Connected,
		})
	}
	app.SetMCPServers(mcpServers)

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
	// Show the RESOLVED composition, not the configured additions: a config
	// that lists middleware.enabled entries used to make this pane display
	// only those entries, hiding every default (evidence_guard, approval,
	// and later intent_loop). ResolvedPipelineNames owns the additive
	// enabled/disabled semantics for the real pipeline.
	pipelineList := pipeline.ResolvedPipelineNames(mc.Enabled, mc.Disabled)
	// The prompt_caching boolean is honored independently of the name
	// lists (an explicit disabled entry wins), so mirror NewFromConfig or
	// the pane would omit a middleware that runs.
	if cfg.Agent.Default.PromptCaching &&
		!slices.Contains(mc.Disabled, "prompt_caching") &&
		!slices.Contains(pipelineList, "prompt_caching") {
		pipelineList = append(pipelineList, "prompt_caching")
	}
	app.SetConfig(
		cfg.Agent.SubAgent.Provider != "" || cfg.Agent.SubAgent.Model != "",
		subProvider,
		cfg.Agent.SubAgent.MaxConcurrency,
		subModel,
		cfg.EmbeddingSearchLive(),
		cfg.Embedding.Model,
		pipelineList,
	)
	app.SetWorld(sess.WorldStatus())

	// Warm the shared model catalog in the background so a slow or
	// unreachable provider endpoint never delays TUI launch; the control
	// channel is buffered and the control loop starts inside app.Run().
	catalog := newModelCatalog(cfg, nil)
	go warmModelList(catalog, func(snap modelSnapshot) { sendModelList(controlCh, snap) })

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
	app.OnModeSelect = func(mode string) {
		if err := sess.SetMode(mode); err != nil {
			app.SetEphemeral(err.Error())
			return
		}
		app.SetMode(mode)
		app.SetEphemeral("session mode: " + mode)
	}

	var cancelAgent context.CancelFunc
	app.OnSubmit = func(input string) {
		ctx, cancel := context.WithCancel(context.Background())
		cancelAgent = cancel
		app.AddPrompt(input)
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
	// The TUI accepts harness-initiated turns: a watch that fires while the
	// terminal is idle starts a turn through the session's run gate. The
	// policy lives here, in the driver, so headless surfaces can differ.
	sess.SetWakeTurnRequester(sess.StartWakeTurn)

	// One shared bridge for questions and approvals (internal/control): the
	// TUI's only job is to be the transport.
	installControlBridges(sess, ctrlSurface{ch: controlCh})

	// Resume durable watches only now that the view, control channel and
	// wake policy are installed; a missed wake must render, not run unheard.
	sess.Start()

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

// modelListAttempts bounds how many discovery retries the warm loop runs. A
// provider that never answers is not worth a session-long probe: the next
// process start runs discovery again from scratch.
const modelListAttempts = 3

// warmModelList publishes the model catalog and keeps it honest.
//
// The first Load is bounded by the catalog's warm budget, so a provider that
// misses that window is absent from the snapshot. While the snapshot is
// unsettled, re-read it after the catalog's cooldown and deliver it again when it
// changed, so a provider slower than the budget does not stay absent for the life
// of the process.
//
// deliver is called once per snapshot worth showing. A caller pushing to a lane
// that can be replaced (the web's SSE stream) resolves the current lane inside
// deliver, so a retry lands wherever the client is by the time it fires.
func warmModelList(catalog *modelCatalog, deliver func(modelSnapshot)) {
	ctx := context.Background()
	snap := catalog.Load(ctx)
	deliver(snap)
	for attempt := 0; attempt < modelListAttempts && !snap.Settled; attempt++ {
		time.Sleep(catalog.retry)
		next := catalog.Current(ctx)
		if !slices.Equal(next.IDs, snap.IDs) {
			deliver(next)
		}
		// Adopt the result either way: a retry that finished discovery without
		// changing the ids is still terminal, and keeping the old snapshot would
		// sleep through the remaining attempts for nothing. A settled snapshot is
		// authoritative, so it may replace the shown list even when the provider
		// now offers fewer models.
		snap = next
	}
}

func sendModelList(ch chan<- control.Msg, snap modelSnapshot) {
	msg := &control.ModelList{Models: snap.IDs, ProviderNames: snap.ProviderNames}
	sendCtrlMsg(ch, nil, msg, tuiSendTimeout)
}
