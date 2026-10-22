package yaah

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/buchenberg/yaah/internal/acp"
	"github.com/buchenberg/yaah/internal/mcp"
	"github.com/buchenberg/yaah/internal/memory"
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
naming an id the store never issued is rejected. See docs/acp.md for
the full surface, persisted state, and client setup details.`,
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
// created or loaded it.
func newACPAgentSession(build acp.BuildOptions) (acp.Session, error) {
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

// acpListSessions adapts the session store to the session/list callback:
// store rows become ACP SessionInfo entries, whose updatedAt is the ISO
// 8601 timestamp of the last persisted activity. A malformed cursor is
// re-flagged as the ACP sentinel so the surface errors with invalid
// params instead of an internal error.
func acpListSessions(store *memory.DB) func(cwdFilter, cursor string) ([]acp.SessionInfo, string, error) {
	return func(cwdFilter, cursor string) ([]acp.SessionInfo, string, error) {
		rows, next, err := store.ListSessionsPage(memory.SessionListFilter{CWD: cwdFilter, Cursor: cursor})
		if err != nil {
			if errors.Is(err, memory.ErrInvalidCursor) {
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
			})
		}
		return out, next, nil
	}
}

// acpDeleteSession adapts the session store to the session/delete
// callback. An unknown id maps to the ACP sentinel: deleting a
// deleted-or-never-created session succeeds silently per spec.
func acpDeleteSession(store *memory.DB) func(id string) error {
	return func(id string) error {
		if err := store.DeleteSession(id); err != nil {
			if errors.Is(err, memory.ErrSessionNotFound) {
				return fmt.Errorf("%w: %s", acp.ErrNoSession, id)
			}
			return err
		}
		return nil
	}
}

func runACPServe(cmd *cobra.Command, args []string) error {
	fmt.Fprintf(os.Stderr, "%s starting ACP server (stdio)...\n", Dim("yaah acp-serve:"))

	// The session store doubles as the lifecycle engine seam: the
	// session/list and session/delete callbacks below share it with the
	// yaah sessions CLI and any future picker surface. Per-conversation
	// agent sessions open their own store handles inside the factory,
	// so this one is used read-and-delete only.
	store, err := memory.OpenDefault()
	if err != nil {
		return fmt.Errorf("open session store: %w", err)
	}
	defer store.Close()

	srv := acp.NewServer(
		newACPAgentSession,
		version,
		func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, Dim("yaah acp-serve:")+" "+format+"\n", args...)
		},
	)
	srv.ListSessions = acpListSessions(store)
	srv.DeleteSession = acpDeleteSession(store)
	return srv.Run(cmd.Context())
}
