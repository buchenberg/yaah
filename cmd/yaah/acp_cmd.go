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
  session/prompt              Run a turn; responds with a stopReason
  session/cancel              Cancel the in-flight turn (notification)
  session/set_mode            Acknowledged; mode switching is not modeled
  session/update              Streaming updates: message/thought chunks,
                              tool calls, plans, usage
  session/request_permission  Tool approvals delegated to the client

One agent conversation is served per process: a session/load for a
foreign session id is rejected, so point the client at a fresh
acp-serve process per conversation.`,
	SilenceErrors: true,
	SilenceUsage:  true,
	Args:          cobra.NoArgs,
	RunE:          runACPServe,
}

func init() {
	rootCmd.AddCommand(acpServeCmd)
}

func runACPServe(cmd *cobra.Command, args []string) error {
	fmt.Fprintf(os.Stderr, "%s starting ACP server (stdio)...\n", Dim("yaah acp-serve:"))

	srv := acp.NewServer(
		func() (acp.Session, error) {
			sess, err := newAgentSession()
			if err != nil {
				return nil, fmt.Errorf("session: %w", err)
			}
			return sess, nil
		},
		version,
		func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, Dim("yaah acp-serve:")+" "+format+"\n", args...)
		},
	)
	return srv.Run(cmd.Context())
}
