package yaah

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/buchenberg/yaah/internal/agent"
	"github.com/buchenberg/yaah/internal/mcp"
	"github.com/buchenberg/yaah/internal/observability"
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

	// Start handles graceful shutdown (via Shutdown) when ctx is
	// cancelled. We wait for it to return so active SSE connections
	// are drained rather than abruptly broken — avoiding
	// ERR_CONNECTION_REFUSED storms on reconnect.
	select {
	case <-ctx.Done():
		fmt.Fprintf(os.Stderr, "%s signal received, shutting down\n", Dim("yaah serve:"))
		// Wait for Start to finish its graceful shutdown.
		if err := <-errCh; err != nil {
			fmt.Fprintf(os.Stderr, "%s HTTP server shutdown: %v\n", Dim("yaah serve:"), err)
		}
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

// runHeadless runs the agent with NoopView and no spinner, suitable for
// MCP serve mode. It accumulates
// conversation state in the session so successive calls form a multi-turn
// dialogue, and returns the response plus the loop's token usage.
func (s *agentSession) runHeadless(ctx context.Context, prompt string) (string, types.Usage, error) {
	rawCompactProvider, compactModel := resolveCompact(s.cfg)
	compactProvider := agent.ResolveCompactProvider(rawCompactProvider, s.cfg.Observability.Otel.Verbose)
	fallbackProvider, fallbackModel, _ := resolveFallback(s.cfg)

	// Snapshot session state under the mutex, mirroring runPrompt's pattern.
	s.mu.RLock()
	prov := s.provider
	mName := s.modelName
	s.mu.RUnlock()

	b := s.loopBuilder(prov, mName, compactProvider, compactModel, fallbackProvider, fallbackModel)

	// Headless serve mode always forces OTel on (in-memory tracing).
	otelForced := true
	loop := b.Build(agent.LoopBuildOptions{
		ApprovalMode: resolveApproval(s.cfg),
		OtelEnabled:  &otelForced,
		OtelVerbose:  s.cfg.Observability.Otel.Verbose,
	})

	response, err := loop.Run(ctx, prompt)

	s.messages = loop.State.Messages
	s.msgIdx = loop.Persister.MsgIdx()

	return response, loop.State.TotalTokens, err
}

// marshalJSON encodes v as indented JSON for tool result payloads.
func marshalJSON(v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
