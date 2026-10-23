package yaah

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/buchenberg/yaah/internal/acp"
	"github.com/buchenberg/yaah/internal/mcp"
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

func runACPServe(cmd *cobra.Command, args []string) error {
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
		func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, Dim("yaah acp-serve:")+" "+format+"\n", args...)
		},
	)
	srv.ListSessions = acpListSessions(reg)
	srv.DeleteSession = acpDeleteSession(reg)
	return srv.Run(cmd.Context())
}
