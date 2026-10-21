package yaah

import (
	"fmt"
	"os"

	"github.com/buchenberg/yaah/internal/acp"
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
  session/prompt              Run a turn; responds with a stopReason
  session/cancel              Cancel the in-flight turn (notification)
  session/set_mode            Acknowledged; mode switching is not modeled
  session/update              Streaming updates: message/thought chunks,
                              tool calls, plans, usage
  session/request_permission  Tool approvals delegated to the client

One agent conversation is served per process: a session/load naming an
id the session store never issued is rejected, and a load for a second
conversation after one is established is rejected. See docs/acp.md for
the full surface, persisted state, and client setup details.`,
	SilenceErrors: true,
	SilenceUsage:  true,
	Args:          cobra.NoArgs,
	RunE:          runACPServe,
}

func init() {
	rootCmd.AddCommand(acpServeCmd)
}

// newACPAgentSession builds an agent session for the ACP server. An
// empty resumeID starts a fresh conversation; otherwise the persisted
// session of that id is restored from the SQLite session store
// (~/.yaah state.db via internal/memory) — that restore is how
// session/load rejects foreign ids and supplies the replay history.
// The ACP client owns the session lifecycle, so --resume from the
// command line is overridden: an acp-serve process always starts
// without a conversation until the client loads one.
func newACPAgentSession(resumeID string) (acp.Session, error) {
	opts := sessionOptionsFromFlags()
	opts.ResumeSessionID = resumeID
	sess, err := newAgentSessionWithOptions(opts, false, false)
	if err != nil {
		return nil, fmt.Errorf("session: %w", err)
	}
	return sess, nil
}

func runACPServe(cmd *cobra.Command, args []string) error {
	fmt.Fprintf(os.Stderr, "%s starting ACP server (stdio)...\n", Dim("yaah acp-serve:"))

	srv := acp.NewServer(
		newACPAgentSession,
		version,
		func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, Dim("yaah acp-serve:")+" "+format+"\n", args...)
		},
	)
	return srv.Run(cmd.Context())
}
