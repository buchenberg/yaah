package yaah

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/buchenberg/yaah/internal/acp"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/mcp"
	"github.com/buchenberg/yaah/internal/providers"
	"github.com/buchenberg/yaah/internal/sessions"
	"github.com/spf13/cobra"
)

// === ACP serve command ===

var acpServeCmd = &cobra.Command{
	Use:   "acp-serve",
	Short: "Expose yaah as an Agent Client Protocol v1 (ACP) agent over stdio",
	Long: `acp-serve starts yaah as an Agent Client Protocol (ACP) v1 agent,
speaking the protocol used by editors such as Zed and VS Code
(https://agentclientprotocol.com).

The protocol uses newline-delimited JSON-RPC 2.0 on stdin/stdout. All
diagnostics go to stderr so stdout stays clean for protocol JSON.

Implemented:
  initialize                  Handshake (protocol version 1)
  authenticate                Validate a declared auth method (method id only;
                              no credential presence check, no network call)
  session/new                 Create a session (honors the client's cwd)
  session/load                Resume a session from ~/.yaah state.db in a
                              fresh process; replays the conversation as
                              session/update notifications before answering
  session/resume              Re-attach to a persisted session without
                              replaying history
  session/list                Page through persisted sessions (cwd filter)
  session/close               Cancel in-flight work and free a session
  session/delete              Remove a session from the store
  session/prompt              Run a turn; responds with a stopReason
  session/cancel              Cancel the in-flight turn (notification)
  session/set_mode            Acknowledged; mode switching is not modeled
  session/update              Streaming updates: message/thought chunks,
                              tool calls, plans, usage
  session/request_permission  Tool approvals delegated to the client

Several conversations may coexist in one process; the persisted session
store (~/.yaah state.db) is shared, so list/resume/close/delete operate
on it the same way the yaah CLI does. A session/load or session/resume
naming an id the store never issued is rejected.

Authentication is by provider credential configured yaah-side
(~/.yaah/config.yaml, or an OAuth token obtained via the web UI login
action):
initialize advertises the accepted method ids as agent-typed authMethods,
and authenticate validates the chosen id without checking that a key is
present. The approval bridge, tool-kind classification, and sandbox
layers are yaah-side policy and are not surfaced through ACP. See
docs/acp.md for the full surface, persisted state, and client setup
details.`,
	SilenceErrors: true,
	SilenceUsage:  true,
	Args:          cobra.NoArgs,
	RunE:          runACPServe,
}

func init() {
	rootCmd.AddCommand(acpServeCmd)
}

// newACPAgentSession builds one agent session for the ACP server. An
// empty ResumeID starts a fresh conversation; otherwise the persisted
// session of that id is restored from the SQLite session store
// (~/.yaah state.db via internal/memory) — that restore is how
// session/load rejects foreign ids and supplies the replay history.
// The ACP client owns the session lifecycle, so --resume from the
// command line is ignored: each session is built from the request that
// created or loaded it. The bare picker form has nothing interactive to
// pick with and is rejected rather than silently ignored.
func newACPAgentSession(build acp.BuildOptions) (acp.Session, error) {
	if resumeSessionID == resumePickerSentinel {
		return nil, fmt.Errorf("--resume without a value opens the interactive picker, which is not available in ACP mode; omit the flag — sessions are named by the client's session/new and session/load requests")
	}
	opts := sessionOptionsFromFlags()
	opts.ResumeSessionID = build.ResumeID
	opts.Cwd = build.Cwd
	opts.MCPManifests = mcpManifestsFromACP(build.MCPServers)
	sess, err := newAgentSessionWithOptions(opts, false, false)
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	return sess, nil
}

// mcpManifestsFromACP converts ACP's stdio server entries to the
// manifest form the MCP client starts.
func mcpManifestsFromACP(refs []acp.McpServerRef) map[string]mcp.Manifest {
	if len(refs) == 0 {
		return nil
	}
	out := make(map[string]mcp.Manifest, len(refs))
	for _, ref := range refs {
		env := make(map[string]string, len(ref.Env))
		for _, e := range ref.Env {
			if e.Name != "" {
				env[e.Name] = e.Value
			}
		}
		out[ref.Name] = mcp.Manifest{
			Command:   ref.Command,
			Args:      ref.Args,
			Env:       env,
			Transport: "stdio",
		}
	}
	return out
}

// acpListSessions adapts the engine-level session registry to the
// session/list callback: registry rows become ACP SessionInfo entries, whose
// updatedAt is the ISO 8601 timestamp of the last persisted activity and whose
// _meta carries the picker's message count. Exactly one registry page is
// served — the callback contract is one page plus its cursor — so this uses
// Page, not List. A malformed cursor is re-flagged as the ACP sentinel so the
// surface errors with invalid params instead of an internal error.
//
// The cwdFilter arrives already canonical (internal/acp canonicalizes it on
// intake); the registry canonicalizes again, which is idempotent and safe.
func acpListSessions(reg *sessions.Registry) func(cwdFilter, cursor string) ([]acp.SessionInfo, string, error) {
	return func(cwdFilter, cursor string) ([]acp.SessionInfo, string, error) {
		rows, next, err := reg.Page(sessions.Filter{CWD: cwdFilter, Cursor: cursor})
		if err != nil {
			if errors.Is(err, sessions.ErrInvalidCursor) {
				return nil, "", fmt.Errorf("%w: %v", acp.ErrInvalidCursor, err)
			}
			return nil, "", err
		}
		out := make([]acp.SessionInfo, 0, len(rows))
		for _, row := range rows {
			out = append(out, acp.SessionInfo{
				SessionID: row.ID,
				Cwd:       row.CWD,
				Title:     row.Title,
				// Store timestamps are Unix seconds (time.Unix origin).
				UpdatedAt: time.Unix(row.UpdatedAt, 0).UTC().Format(time.RFC3339),
				Meta:      sessionMeta(row.MessageCount),
			})
		}
		return out, next, nil
	}
}

// sessionMeta builds the ACP `_meta` payload for one session/list entry.
// A session with no persisted messages reports no messageCount at all
// rather than a zero, so a client can tell "empty" from "unknown".
func sessionMeta(messageCount int) *acp.SessionMeta {
	if messageCount <= 0 {
		return nil
	}
	return &acp.SessionMeta{MessageCount: messageCount}
}

// acpDeleteSession adapts the session registry to the session/delete
// callback. An unknown id maps to the ACP sentinel: deleting a
// deleted-or-never-created session succeeds silently per spec.
func acpDeleteSession(reg *sessions.Registry) func(id string) error {
	return func(id string) error {
		if err := reg.Delete(id); err != nil {
			if errors.Is(err, sessions.ErrNotFound) {
				return fmt.Errorf("%w: %s", acp.ErrNoSession, id)
			}
			return err
		}
		return nil
	}
}

// === ACP model catalog ===

// acpModelCatalogBudget bounds the single network round of model discovery
// the ACP catalog performs. A provider endpoint that is slow or
// unreachable must not hold up a client, so the fetch is abandoned at the
// deadline and the catalog keeps whatever config already declares.
const acpModelCatalogBudget = 2 * time.Second

// acpModelCatalogRetry is how long an incomplete discovery is cached
// before a later request may retry it. Without the cooldown a provider
// that was unreachable at startup would make every session/new pay the
// full budget again.
const acpModelCatalogRetry = 30 * time.Second

// newACPModelCatalog builds the process-lifetime model catalog handed to
// the ACP server. Discovery reuses the provider machinery behind the TUI
// model picker and the web command palette (providers.FetchAllModels with
// makeModelLister), so ACP offers exactly the models those surfaces offer
// rather than a second, drifting code path.
//
// A config that cannot be loaded, or that declares no providers, disables
// the feature: the catalog is nil, which the ACP server treats as "no
// model picker", and serve carries on. Nothing here can fail acp-serve.
// Discovery starts here, off the request path, so the first session/new
// normally finds a warm catalog instead of paying for the network round.
func newACPModelCatalog(logf func(format string, args ...any)) acp.ModelCatalog {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	cfg, err := config.Load()
	if err != nil {
		logf("model catalog disabled: %v", err)
		return nil
	}
	if len(cfg.Providers) == 0 {
		logf("model catalog disabled: no providers configured")
		return nil
	}
	cat := &acpModelCatalog{cfg: cfg, logf: logf, budget: acpModelCatalogBudget}
	go cat.Options(context.Background())
	return cat
}

// acpModelCatalog is the ACP server's model catalog: the providers' model
// lists reduced to the flat provider/model options a picker shows.
//
// Discovery is two-phase. The fast path answers from config alone — every
// model a provider declares under models: is offered with no network call,
// so a picker always has content. The slow path then asks each provider
// that declares no models to list its own; it runs once, under
// acpModelCatalogBudget, and the result is cached for the process
// lifetime. Discovery failures are logged and dropped: model discovery is
// a convenience and never fails a session.
type acpModelCatalog struct {
	cfg    *config.Config
	logf   func(format string, args ...any)
	budget time.Duration

	mu       sync.Mutex
	settled  bool
	options  []acp.ModelOption
	inflight chan struct{} // non-nil while a discovery attempt runs
	lastTry  time.Time

	// discoverFn replaces the network phase in tests. It must be set
	// before the first Options call.
	discoverFn func(ctx context.Context) ([]acp.ModelOption, bool)
}

// Options implements acp.ModelCatalog. The slow path runs in one shared
// goroutine so concurrent callers wait on the same attempt rather than
// starting one each; the budget bounds that attempt. A caller's ctx only
// bounds its own wait — an attempt already in flight keeps running and
// publishes its result for the next request, so a client disconnect or
// shutdown does not throw away discovery.
func (c *acpModelCatalog) Options(ctx context.Context) []acp.ModelOption {
	if c == nil || c.cfg == nil {
		return nil
	}
	c.mu.Lock()
	if c.settled {
		opts := c.options
		c.mu.Unlock()
		return opts
	}
	done := c.inflight
	if done == nil {
		// An incomplete attempt is retried at most once per cooldown, so
		// a provider that is down at startup cannot make every request
		// pay the full budget.
		if !c.lastTry.IsZero() && time.Since(c.lastTry) < acpModelCatalogRetry {
			opts := c.options
			c.mu.Unlock()
			return opts
		}
		c.lastTry = time.Now()
		done = make(chan struct{})
		c.inflight = done
		go c.refresh(done)
	}
	c.mu.Unlock()

	select {
	case <-done:
	case <-ctx.Done():
	}
	c.mu.Lock()
	opts := c.options
	c.mu.Unlock()
	return opts
}

// refresh runs one discovery attempt and publishes the result. It holds
// no lock across the network call, so callers wait on the channel instead
// of on a mutex.
func (c *acpModelCatalog) refresh(done chan struct{}) {
	defer close(done)
	discover := c.discover
	if c.discoverFn != nil {
		discover = c.discoverFn
	}
	opts, complete := discover(context.Background())
	// Config-declared models are worth keeping even when the fetch did not
	// finish, but the slow path is only marked settled once it completed:
	// a caller whose wait was abandoned must not leave the catalog stuck
	// at the static subset for the rest of the process.
	c.mu.Lock()
	c.options = opts
	c.settled = complete
	c.inflight = nil
	c.mu.Unlock()
}

// discover builds the option list — config-declared models first, then
// whatever the providers report — and reports whether the network phase
// finished inside the budget, which is what makes the result cacheable.
func (c *acpModelCatalog) discover(ctx context.Context) (options []acp.ModelOption, complete bool) {
	seen := make(map[string]bool, len(c.cfg.Providers))
	for _, name := range sortedProviderNames(c.cfg) {
		for _, m := range c.cfg.Providers[name].Models {
			if m.Name == "" {
				continue
			}
			id := name + "/" + m.Name
			if seen[id] {
				continue
			}
			seen[id] = true
			options = append(options, c.option(id, name, m.Name))
		}
	}

	fetchCtx, cancel := context.WithTimeout(ctx, c.budget)
	defer cancel()
	for _, id := range providers.FetchAllModels(fetchCtx, c.cfg, makeModelLister) {
		if seen[id] {
			continue
		}
		seen[id] = true
		providerName, modelName := splitModelID(id)
		options = append(options, c.option(id, providerName, modelName))
	}
	if err := fetchCtx.Err(); err != nil {
		c.logf("model catalog: discovery incomplete (%v), offering %d model(s) from config", err, len(options))
		return options, false
	}
	return options, true
}

// option builds one catalog entry. The label qualifies the model with the
// provider's display name when config sets one (providers[].name), falling
// back to the provider key, so a picker shows "claude-sonnet-4 (Anthropic)"
// instead of a bare slug.
func (c *acpModelCatalog) option(id, providerName, modelName string) acp.ModelOption {
	label := providerName
	if p, ok := c.cfg.Providers[providerName]; ok && p.Name != "" {
		label = p.Name
	}
	return acp.ModelOption{
		Provider:    providerName,
		Model:       modelName,
		Name:        modelName + " (" + label + ")",
		Description: "",
	}
}

// sortedProviderNames lists the configured provider keys in stable order
// so the picker's contents do not shuffle between processes.
func sortedProviderNames(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// splitModelID splits a "provider/model" id. Only the first separator
// belongs to the provider: model names may contain slashes of their own
// (e.g. "prism-ml/Bonsai-27B-gguf:Q1_0").
func splitModelID(id string) (providerName, modelName string) {
	if i := strings.Index(id, "/"); i >= 0 {
		return id[:i], id[i+1:]
	}
	return id, id
}

func runACPServe(cmd *cobra.Command, args []string) error {
	logf := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, Dim("yaah acp-serve:")+" "+format+"\n", args...)
	}
	fmt.Fprintf(os.Stderr, "%s starting ACP server (stdio)...\n", Dim("yaah acp-serve:"))

	// The engine-level session registry is the lifecycle seam: the
	// session/list and session/delete callbacks below share it with the
	// yaah sessions CLI, the web session picker, and the interactive
	// resume picker. Per-conversation agent sessions open their own store
	// handles inside the factory, so this one is used read-and-delete only.
	reg, err := sessions.OpenDefault()
	if err != nil {
		return fmt.Errorf("open session store: %w", err)
	}
	defer reg.Close()

	srv := acp.NewServer(
		newACPAgentSession,
		version,
		logf,
		newACPModelCatalog(logf),
	)
	srv.ListSessions = acpListSessions(reg)
	srv.DeleteSession = acpDeleteSession(reg)
	return srv.Run(cmd.Context())
}
