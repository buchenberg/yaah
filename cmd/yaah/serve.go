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

// serveCmd exposes the yaah engine as an MCP tool server over stdio so other
// agents (or benchmarking harnesses) can drive multi-turn conversations and
// query in-process OpenTelemetry traces programmatically.
//
// Protocol: newline-delimited JSON-RPC 2.0 on stdin/stdout. All diagnostics
// (banners, warnings, logs) go to stderr so stdout stays clean for the MCP
// client.
var serveCmd = newMCPServeCmd(true)

// mcpServeCmd is the canonical spelling: the MCP server lives under the mcp
// group, next to the other MCP surface (list/add/remove). 'yaah serve' remains
// as a deprecated alias because MCP client configs and benchmarking harnesses
// reference it by name.
var mcpServeCmd = newMCPServeCmd(false)

func newMCPServeCmd(deprecated bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Expose the yaah engine as an MCP tool server (stdio, or HTTP with --listen)",
		Long: `serve starts yaah as an MCP (Model Context Protocol) tool server,
reading newline-delimited JSON-RPC 2.0 from stdin and writing responses to
stdout. It exposes three tools:

  prompt   Run a multi-turn agent prompt; conversation state persists
           across calls for the lifetime of the server.
  traces   Query in-process OpenTelemetry spans (flat list or per-trace tree).
  status   Report provider, model, session, message count, and token usage.

By default the transport is stdio. Passing --listen <addr> switches to the
Streamable HTTP transport instead (stdio is not served in that mode).

All diagnostics are written to stderr; stdout carries only protocol JSON.
This mode is intended for agent-to-agent communication and benchmarking.`,
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
		RunE:          runServe,
	}
	registerServeFlags(cmd)
	if deprecated {
		cmd.Deprecated = "use 'yaah mcp serve' instead"
	}
	return cmd
}

func init() {
	rootCmd.AddCommand(serveCmd)
	mcpCmd.AddCommand(mcpServeCmd)
}

// listenAddr is the listen address for the Streamable HTTP transport.
// Empty (default) preserves the stdio-only behavior.
var listenAddr string

// httpAddr is the deprecated --http alias; kept working, warns once.
var httpAddr string

// serveToken authenticates the HTTP transport. Empty generates a
// random token at startup and prints it once.
var serveToken string

// serveAllowUnknownSessions opts into restart-transparency: stale
// session IDs are re-registered instead of rejected. Strict validation
// is the default; the bearer token plus a fresh initialize handshake
// cover the restart case for spec-compliant clients.
var serveAllowUnknownSessions bool

func registerServeFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&listenAddr, "listen", "", "expose MCP over Streamable HTTP at this address (e.g. 127.0.0.1:7333); empty keeps stdio transport")
	cmd.Flags().StringVar(&httpAddr, "http", "", "deprecated: use --listen")
	_ = cmd.Flags().MarkHidden("http")
	cmd.Flags().StringVar(&serveToken, "token", "", "bearer token required by --listen endpoints (randomly generated when empty)")
	cmd.Flags().BoolVar(&serveAllowUnknownSessions, "allow-unknown-sessions", false, "--listen: accept stale/unknown Mcp-Session-Id values (server restart transparency); rejected by default")
}

// serveState bundles the state shared by both serve transports: the
// serialized tool-handler mutex, usage counters, and the lazy agent
// session (review A2).
type serveState struct {
	opts        SessionOptions
	buf         *observability.BufferingSpanProcessor
	mu          sync.Mutex
	totalTokens types.Usage
	promptCount int
	sess        *agentSession
	sessErr     error
}

// ensureSession lazily builds the heavy agent session (config, DB,
// skills, MCP clients) on first use so the MCP initialize handshake
// completes instantly.
func (st *serveState) ensureSession() error {
	if st.sess != nil {
		return nil
	}
	st.sess, st.sessErr = newAgentSessionWithOptions(st.opts, false, false)
	if st.sessErr != nil {
		return st.sessErr
	}
	fmt.Fprintf(os.Stderr, "  %s %s/%s\n", Dim("provider:"), st.sess.providerName, st.sess.modelName)
	fmt.Fprintf(os.Stderr, "  %s %s\n", Dim("session:"), st.sess.sessionID)
	return nil
}

// runServeTransport builds the shared MCP bootstrap — in-memory tracing
// inputs, serve session options, lazy session, and tool registration —
// then hands the server to the transport-specific start function. Only
// the listener setup differs between transports (review A2).
func runServeTransport(start func(st *serveState, srv *mcp.Server) error) error {
	buf := observability.NewBufferingSpanProcessor()
	opts := sessionOptionsFromFlags()
	opts.OtelProcessors = []sdktrace.SpanProcessor{buf}
	opts.OtelInMemoryOnly = true
	st := &serveState{opts: opts, buf: buf}

	srv := mcp.NewServer("yaah", version)
	registerServeTools(srv, &st.mu, &st.totalTokens, &st.promptCount, buf, &st.sess, &st.sessErr, st.ensureSession)
	return start(st, srv)
}

func runServe(cmd *cobra.Command, args []string) error {
	addr, err := resolveListenAddr(cmd, "listen", "http", listenAddr, httpAddr)
	if err != nil {
		return err
	}
	httpAddr = addr
	if httpAddr != "" {
		fmt.Fprintf(os.Stderr, "%s starting MCP tool server (HTTP at %s/mcp)...\n", Dim("yaah serve:"), httpAddr)
		return runServeTransport(serveHTTPTransport)
	}
	fmt.Fprintf(os.Stderr, "%s starting MCP tool server (stdio)...\n", Dim("yaah serve:"))
	return runServeTransport(serveStdioTransport)
}

// serveStdioTransport runs the MCP server on newline-delimited JSON-RPC
// over stdin/stdout with graceful shutdown on SIGINT/SIGTERM.
func serveStdioTransport(st *serveState, srv *mcp.Server) error {
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

	if st.sess != nil {
		st.sess.close()
	}
	return runErr
}

// serveHTTPTransport exposes the MCP server over Streamable HTTP. The
// dev-loop ergonomics are intentional: a developer can rebuild yaah,
// kill the previous process, and re-run without restarting the MCP
// client (Kilo).
func serveHTTPTransport(st *serveState, srv *mcp.Server) error {
	// Warm up the agent session in the background so the first prompt
	// call doesn't time out waiting for config/DB/skills/MCP clients.
	go func() {
		st.mu.Lock()
		defer st.mu.Unlock()
		if err := st.ensureSession(); err != nil {
			fmt.Fprintf(os.Stderr, "%s session warmup failed: %v\n", Dim("yaah serve:"), err)
		}
	}()

	httpSrv := mcp.NewHTTPServer(srv, httpAddr)

	// Auth gate: every --http endpoint except /health requires the
	// bearer token. Generate one when the operator did not pin it so
	// `yaah serve --http` is never accidentally unauthenticated.
	token := serveToken
	if token == "" {
		var err error
		token, err = newWebToken()
		if err != nil {
			return fmt.Errorf("generate serve auth token: %w", err)
		}
	}
	httpSrv.SetAuthToken(token)
	httpSrv.SetAllowUnknownSessions(serveAllowUnknownSessions)
	fmt.Fprintf(os.Stderr, "%s bearer token: %s\n", Dim("yaah serve:"), token)
	fmt.Fprintf(os.Stderr, "%s clients send it as %s\n", Dim("yaah serve:"), `Authorization: Bearer <token>`)

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

	if st.sess != nil {
		st.sess.close()
	}
	return nil
}

// runHeadless runs the agent with NoopView and no spinner, suitable for
// MCP serve mode. It accumulates
// conversation state in the session so successive calls form a multi-turn
// dialogue, and returns the response plus the loop's token usage.
func (s *agentSession) runHeadless(ctx context.Context, prompt string) (string, types.Usage, error) {
	var (
		response string
		usage    types.Usage
		err      error
	)
	// Serve prompts mutate the same transcript as interactive runs; take
	// the run gate so a concurrent prompt or compact cannot fork it.
	s.serialize(func() {
		response, usage, err = s.runHeadlessLocked(ctx, prompt)
	})
	return response, usage, err
}

func (s *agentSession) runHeadlessLocked(ctx context.Context, prompt string) (string, types.Usage, error) {
	compactProvider, compactModel, fallbackProvider, fallbackModel, fallbackProviderName := s.auxProviders()

	// Snapshot session state under the mutex, mirroring runPrompt's pattern.
	s.mu.RLock()
	prov := s.provider
	mName := s.modelName
	s.mu.RUnlock()

	b := s.loopBuilder(prov, mName, compactProvider, compactModel, fallbackProvider, fallbackModel, fallbackProviderName)

	// Headless serve mode always forces OTel on (in-memory tracing).
	otelForced := true
	loop := b.Build(agent.LoopBuildOptions{
		ApprovalMode: resolveApproval(s.cfg, s.opts),
		OtelEnabled:  &otelForced,
		OtelVerbose:  s.cfg.Observability.Otel.Verbose,
	})

	response, err := loop.Run(ctx, prompt)

	s.mu.Lock()
	s.messages = loop.State.Messages
	s.msgIdx = loop.Persister.MsgIdx()
	s.mu.Unlock()

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
