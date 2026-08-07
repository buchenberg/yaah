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
	Short: "Expose yaah as an ACP (Agent Communication Protocol) server over stdio",
	Long: `acp-serve starts yaah as an ACP protocol server over stdio,
implementing the Agent Communication Protocol used by Gas Town and other
orchestrators.

The protocol uses newline-delimited JSON-RPC 2.0 on stdin/stdout. All
diagnostics go to stderr so stdout stays clean for protocol JSON.

Methods implemented:
  initialize                  Handshake with client
  notifications/initialized   Client confirms init complete
  session/new                 Create a conversation session
  session/prompt              Send a prompt, receive streaming updates
  session/cancel              Cancel the current turn
  session/set_mode            Change agent mode (also used as heartbeat)
  tools/list                  List available tools
  tools/call                  Execute a tool`,
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

	sess, err := newAgentSession()
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}
	defer sess.Close()

	srv := acp.NewServer(sess, version, func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, Dim("yaah acp-serve:")+" "+format+"\n", args...)
	})
	return srv.Run(cmd.Context())
}
