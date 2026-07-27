package yaah

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/mcp"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/observability"
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/types"
	"github.com/spf13/cobra"
)

// extraOtelProcessors holds span processors injected into the
// observability.Setup call made by newAgentSession. Serve mode uses this to
// attach an in-memory BufferingSpanProcessor so traces can be queried via
// the `traces` MCP tool without an external Jaeger/OTLP backend.
var extraOtelProcessors []sdktrace.SpanProcessor

// otelInMemoryOnly, when true, tells newAgentSession to activate tracing
// with no OTLP endpoint — spans flow only to extraOtelProcessors. Set by
// serve mode so it captures traces regardless of the user's otel config and
// without requiring a collector to be running.
var otelInMemoryOnly bool

// serveCmd exposes the yaah engine as an MCP tool server over stdio so other
// agents (or benchmarking harnesses) can drive multi-turn conversations and
// query in-process OpenTelemetry traces programmatically.
//
// Protocol: newline-delimited JSON-RPC 2.0 on stdin/stdout. All diagnostics
// (banners, warnings, logs) go to stderr so stdout stays clean for the MCP
// client.
var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Expose the yaah engine as an MCP tool server over stdio",
	Long: `serve starts yaah as an MCP (Model Context Protocol) tool server,
reading newline-delimited JSON-RPC 2.0 from stdin and writing responses to
stdout. It exposes three tools:

  prompt   Run a multi-turn agent prompt; conversation state persists
           across calls for the lifetime of the server.
  traces   Query in-process OpenTelemetry spans (flat list or per-trace tree).
  status   Report provider, model, session, message count, and token usage.

All diagnostics are written to stderr; stdout carries only protocol JSON.
This mode is intended for agent-to-agent communication and benchmarking.`,
	SilenceErrors: true,
	SilenceUsage:  true,
	Args:          cobra.NoArgs,
	RunE:          runServe,
}

func init() {
	rootCmd.AddCommand(serveCmd)
}

// httpAddr is the listen address for the Streamable HTTP transport.
// Empty (default) preserves the stdio-only behavior.
var httpAddr string

func init() {
	serveCmd.Flags().StringVar(&httpAddr, "http", "", "expose MCP over Streamable HTTP at this address (e.g. 127.0.0.1:7333); empty keeps stdio transport")
}

func runServe(cmd *cobra.Command, args []string) error {
	// Activate in-memory tracing before the session is built so the
	// BufferingSpanProcessor is attached to the global TracerProvider.
	buf := observability.NewBufferingSpanProcessor()
	extraOtelProcessors = []sdktrace.SpanProcessor{buf}
	otelInMemoryOnly = true
	defer func() {
		extraOtelProcessors = nil
		otelInMemoryOnly = false
	}()

	if httpAddr != "" {
		fmt.Fprintf(os.Stderr, "%s starting MCP tool server (HTTP at %s/mcp)...\n", Dim("yaah serve:"), httpAddr)
		return runServeHTTP(buf)
	}

	fmt.Fprintf(os.Stderr, "%s starting MCP tool server (stdio)...\n", Dim("yaah serve:"))

	// mu serializes tool handlers against the shared session state. The MCP
	// server dispatches serially today, but the guard keeps serve correct if
	// dispatch ever becomes concurrent.
	var mu sync.Mutex
	var totalTokens types.Usage
	promptCount := 0

	// Lazy session: the MCP server starts immediately so the initialize
	// handshake completes instantly. The heavy agent session (config, DB,
	// skills, MCP clients) is built on the first prompt call.
	var sess *agentSession
	var sessErr error
	ensureSession := func() error {
		if sess != nil {
			return nil
		}
		sess, sessErr = newAgentSession()
		if sessErr != nil {
			return sessErr
		}
		fmt.Fprintf(os.Stderr, "  %s %s/%s\n", Dim("provider:"), sess.providerName, sess.modelName)
		fmt.Fprintf(os.Stderr, "  %s %s\n", Dim("session:"), sess.sessionID)
		return nil
	}

	srv := mcp.NewServer("yaah", version)
	registerServeTools(srv, &mu, &totalTokens, &promptCount, buf, &sess, &sessErr, ensureSession)

	// Graceful shutdown on SIGINT/SIGTERM. EOF on stdin (parent exit) also
	// terminates Serve naturally.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(ctx, os.Stdin, os.Stdout)
	}()

	var runErr error
	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			runErr = err
		}
		fmt.Fprintf(os.Stderr, "%s stdin closed, shutting down\n", Dim("yaah serve:"))
	case <-ctx.Done():
		fmt.Fprintf(os.Stderr, "%s signal received, shutting down\n", Dim("yaah serve:"))
	}

	if sess != nil {
		sess.close()
	}
	return runErr
}

// runServeHTTP exposes the MCP server over Streamable HTTP. It
// mirrors runServe's tool registration but listens on a TCP port
// instead of speaking JSON-RPC on stdio. The dev-loop ergonomics
// are intentional: a developer can rebuild yaah, kill the previous
// process, and re-run without restarting the MCP client (Kilo).
func runServeHTTP(buf *observability.BufferingSpanProcessor) error {
	var mu sync.Mutex
	var totalTokens types.Usage
	promptCount := 0

	var sess *agentSession
	var sessErr error
	ensureSession := func() error {
		if sess != nil {
			return nil
		}
		sess, sessErr = newAgentSession()
		if sessErr != nil {
			return sessErr
		}
		fmt.Fprintf(os.Stderr, "  %s %s/%s\n", Dim("provider:"), sess.providerName, sess.modelName)
		fmt.Fprintf(os.Stderr, "  %s %s\n", Dim("session:"), sess.sessionID)
		return nil
	}

	srv := mcp.NewServer("yaah", version)
	registerServeTools(srv, &mu, &totalTokens, &promptCount, buf, &sess, &sessErr, ensureSession)

	// Warm up the agent session in the background so the first prompt
	// call doesn't time out waiting for config/DB/skills/MCP clients.
	go func() {
		mu.Lock()
		defer mu.Unlock()
		if err := ensureSession(); err != nil {
			fmt.Fprintf(os.Stderr, "%s session warmup failed: %v\n", Dim("yaah serve:"), err)
		}
	}()

	httpSrv := mcp.NewHTTPServer(srv, httpAddr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.Start(ctx) }()

	select {
	case <-ctx.Done():
		fmt.Fprintf(os.Stderr, "%s signal received, shutting down\n", Dim("yaah serve:"))
	case err := <-errCh:
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s HTTP server error: %v\n", Dim("yaah serve:"), err)
		}
	}

	if sess != nil {
		sess.close()
	}
	return nil
}

// registerServeTools attaches the prompt, traces, and status tools to
// srv. It is shared between the stdio and HTTP transports so the two
// stay in lockstep.
func registerServeTools(
	srv *mcp.Server,
	mu *sync.Mutex,
	totalTokens *types.Usage,
	promptCount *int,
	buf *observability.BufferingSpanProcessor,
	sessPtr **agentSession,
	sessErrPtr *error,
	ensureSession func() error,
) {
	srv.AddTool(mcp.ServerToolDef{
		Name:        "prompt",
		Description: "Run an agent prompt. Conversation state persists across calls, enabling multi-turn dialogue. Returns the assistant's final response text.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"message":{"type":"string","description":"The user prompt to send to the agent"}},"required":["message"]}`),
		Handler: func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
			var p struct {
				Message string `json:"message"`
			}
			if len(rawArgs) > 0 {
				if err := json.Unmarshal(rawArgs, &p); err != nil {
					return "", fmt.Errorf("invalid arguments: %w", err)
				}
			}
			if p.Message == "" {
				return "", fmt.Errorf("message is required")
			}

			mu.Lock()
			defer mu.Unlock()
			if err := ensureSession(); err != nil {
				return "", fmt.Errorf("session init: %w", err)
			}
			sess := *sessPtr
			start := time.Now()
			resp, usage, err := sess.runHeadless(ctx, p.Message)
			totalTokens.PromptTokens += usage.PromptTokens
			totalTokens.CompletionTokens += usage.CompletionTokens
			totalTokens.TotalTokens += usage.TotalTokens
			*promptCount++
			fmt.Fprintf(os.Stderr, "  %s prompt #%d (%s)\n", Dim("yaah serve:"), *promptCount, formatDuration(time.Since(start)))
			if err != nil {
				return "", err
			}
			return resp, nil
		},
	})

	srv.AddTool(mcp.ServerToolDef{
		Name:        "traces",
		Description: "Query in-process OpenTelemetry spans captured during prompt execution. Optionally filter by trace_id and render as a parent-child tree.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"trace_id":{"type":"string","description":"Return only spans belonging to this trace ID"},"tree":{"type":"boolean","description":"When true (with trace_id), render spans as a nested parent-child tree"},"limit":{"type":"integer","description":"Return only the most recent N spans (flat mode)"}}}`),
		Handler: func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
			var p struct {
				TraceID string `json:"trace_id"`
				Tree    bool   `json:"tree"`
				Limit   int    `json:"limit"`
			}
			if len(rawArgs) > 0 {
				if err := json.Unmarshal(rawArgs, &p); err != nil {
					return "", fmt.Errorf("invalid arguments: %w", err)
				}
			}

			if p.TraceID != "" && p.Tree {
				nodes := buf.TraceTree(p.TraceID)
				return marshalJSON(nodes)
			}

			spans := buf.Traces()
			if p.TraceID != "" {
				filtered := spans[:0]
				for _, s := range spans {
					if s.TraceID == p.TraceID {
						filtered = append(filtered, s)
					}
				}
				spans = filtered
			}
			if p.Limit > 0 && len(spans) > p.Limit {
				spans = spans[len(spans)-p.Limit:]
			}
			return marshalJSON(spans)
		},
	})

	srv.AddTool(mcp.ServerToolDef{
		Name:        "status",
		Description: "Report the server's provider, model, session ID, conversation length, cumulative token usage, and buffered span count.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			status := map[string]any{
				"prompts":           *promptCount,
				"prompt_tokens":     totalTokens.PromptTokens,
				"completion_tokens": totalTokens.CompletionTokens,
				"total_tokens":      totalTokens.TotalTokens,
				"spans_buffered":    len(buf.Traces()),
				"session_ready":     *sessPtr != nil,
				"pid":               os.Getpid(),
			}
			sess := *sessPtr
			if sess != nil {
				status["provider"] = sess.providerName
				status["model"] = sess.modelName
				status["session_id"] = sess.sessionID
				status["messages"] = len(sess.messages)
			}
			return marshalJSON(status)
		},
	})
}

// (NoopView) and no spinner, suitable for MCP serve mode. It accumulates
// conversation state in the session so successive calls form a multi-turn
// dialogue, and returns the response plus the loop's token usage.
func (s *agentSession) runHeadless(ctx context.Context, prompt string) (string, types.Usage, error) {
	compactProvider, compactModel := resolveCompact(s.cfg)
	fallbackProvider, fallbackModel := resolveFallback(s.cfg)

	loop := agent.NewLoop(s.provider, s.toolReg,
		agent.WithModel(s.modelName),
		agent.WithSystemPrompt(s.systemPrompt),
		agent.WithView(agent.NoopView{}),
		agent.WithMessages(s.messages),
		agent.WithDB(s.db),
		agent.WithWriteDebouncer(func() *memory.DebouncedWriter {
			if s.db != nil {
				return memory.NewDebouncedWriter(s.db)
			}
			return nil
		}()),
		agent.WithSessionID(s.sessionID),
		agent.WithMsgIdx(s.msgIdx),
		agent.WithHookDir(s.cfg.Hooks.Dir),
		agent.WithFallback(fallbackProvider, fallbackModel),
		agent.WithCompactProvider(compactProvider, compactModel),
		agent.WithApprovalMode(resolveApproval(s.cfg)),
		agent.WithPipeline(s.cfg.Agent.Middleware.Enabled, s.cfg.Agent.Middleware.Disabled),
		agent.WithSteer(s.steerCh),
		agent.WithFollowUps(s.followupCh),
		agent.WithConflictTracker(s.tracker),
		agent.WithToolsLevel(agent.FullTools),
		agent.WithOtel(true, s.cfg.Observability.Otel.Verbose),
		agent.WithSubAgentConcurrency(
			s.cfg.Agent.SubAgent.MaxConcurrency,
			time.Duration(s.cfg.Agent.SubAgent.StuckChildTimeout)*time.Second,
			buildStuckChildTimeouts(s.cfg.Agent.SubAgent),
		),
		agent.WithLoopConfig(agent.LoopConfig{
			MaxIterations:          s.cfg.Agent.Default.MaxIterations,
			MaxTurns:               s.cfg.Agent.Default.MaxTurns,
			MaxRetries:             s.cfg.Agent.Default.MaxRetries,
			RetryBackoffSecs:       s.cfg.Agent.Default.RetryBackoffSecs,
			ContextWindow:          providers.ResolveWindow(s.modelName, s.cfg.Agent.Default.ContextWindow),
			CompactionThreshold:    s.cfg.Agent.Default.CompactionThreshold,
			RawCompactionThreshold: s.cfg.Agent.Default.RawCompactionThreshold,
			EstimateFactor:         s.cfg.Agent.Default.EstimateFactor,
			LoopDetectCount:        s.cfg.Agent.Default.LoopDetectCount,
			LoopDetectWindow:       s.cfg.Agent.Default.LoopDetectWindow,
			MaxToolConcurrency:     s.cfg.Agent.Default.MaxToolConcurrency,
			MaxInlineToolsPerTurn:  s.cfg.Agent.Default.MaxInlineToolsPerTurn,
			PromptCaching:          s.cfg.Agent.Default.PromptCaching,
			ReasoningProtectTurns:  s.cfg.Agent.Default.ReasoningProtect,
			ToolResultMaxLines:     s.cfg.Agent.Default.ToolResultMaxLines,
			ToolResultMaxBytes:     s.cfg.Agent.Default.ToolResultMaxBytes,
			PruneProtectTokens:     s.cfg.Agent.Default.PruneProtectTokens,
			PruneMinReclaim:        s.cfg.Agent.Default.PruneMinReclaim,
			PruneMinTurns:          s.cfg.Agent.Default.PruneMinTurns,
		}),
	)

	response, err := loop.Run(ctx, prompt)

	s.messages = loop.Messages
	s.msgIdx = loop.MsgIdx

	return response, loop.TotalTokens, err
}

// marshalJSON encodes v as indented JSON for tool result payloads.
func marshalJSON(v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
