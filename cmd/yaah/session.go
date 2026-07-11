package yaah

import (
	"fmt"
	"time"

	"github.com/buchenberg/yaah/internal/memory"
	"github.com/spf13/cobra"
)

// sessionCmd is the `yaah session` subcommand tree.
var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Manage conversation sessions",
}

// sessionListCmd lists recent sessions.
var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent sessions",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := memory.OpenDefault()
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer db.Close()

		sessions, err := db.ListSessions(10)
		if err != nil {
			return fmt.Errorf("list sessions: %w", err)
		}

		if len(sessions) == 0 {
			cmd.Println("No sessions found.")
			return nil
		}

		cmd.Printf("Recent sessions:\n\n")
		for _, s := range sessions {
			started := time.Unix(s.StartedAt, 0).Format("2006-01-02 15:04")
			model := s.Model
			if model == "" {
				model = "unknown"
			}
			cmd.Printf("  %s\n", Bold(s.ID))
			cmd.Printf("        %s\n", Dim(fmt.Sprintf("started: %s | model: %s | cwd: %s", started, model, s.CWD)))
		}
		return nil
	},
}

// sessionShowCmd shows messages in a session.
var sessionShowCmd = &cobra.Command{
	Use:   "show <session-id>",
	Short: "Show messages in a session",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := memory.OpenDefault()
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer db.Close()

		msgs, err := db.GetMessages(args[0])
		if err != nil {
			return fmt.Errorf("get messages: %w", err)
		}

		if len(msgs) == 0 {
			cmd.Printf("No messages in session %s\n", args[0])
			return nil
		}

		cmd.Printf("Session %s (%d messages):\n\n", args[0], len(msgs))
		for _, m := range msgs {
			ts := time.Unix(m.Timestamp, 0).Format("15:04:05")
			switch m.Role {
			case "user":
				cmd.Printf("  %s %s\n", Dim(ts), Bold(m.Content))
			case "assistant":
				cmd.Printf("  %s %s\n", Dim(ts), m.Content)
			case "tool":
				cmd.Printf("  %s %s %s\n", Dim(ts), Dim("tool:"+m.ToolName), Dim(m.Content))
			default:
				cmd.Printf("  %s [%s] %s\n", Dim(ts), m.Role, m.Content)
			}
		}
		return nil
	},
}

func init() {
	sessionCmd.AddCommand(sessionListCmd)
	sessionCmd.AddCommand(sessionShowCmd)
	rootCmd.AddCommand(sessionCmd)
}
