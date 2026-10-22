package yaah

import (
	"errors"
	"fmt"
	"time"

	"github.com/buchenberg/yaah/internal/acp"
	"github.com/buchenberg/yaah/internal/memory"
	"github.com/spf13/cobra"
)

// sessionsCmd is the `yaah sessions` subcommand tree: direct access to
// the persisted conversation store from the CLI. ACP clients reach the
// same rows through session/list and session/delete (acp session
// lifecycle); TUI/REPL resume pickers are planned follow-ups on the
// same store seam.
var sessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "Inspect the persisted conversation sessions",
}

// sessionsListCmd lists recent sessions, newest first, paged the same
// way session/list pages (title, last activity, cwd, message count).
var sessionsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List recent conversation sessions",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwdFlag, _ := cmd.Flags().GetString("cwd")
		limit, _ := cmd.Flags().GetInt("limit")
		if limit <= 0 {
			return fmt.Errorf("--limit must be positive")
		}
		// Same canonicalization session/list applies, so the CLI and ACP
		// filters match the stored session cwds identically.
		cwd, err := acp.CanonicalFilterCwd(cwdFlag)
		if err != nil {
			return fmt.Errorf("--cwd: %v", err)
		}

		db, err := memory.OpenDefault()
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer db.Close()

		// Page until limit rows or the last page; the store enforces its
		// own page size, the CLI cursor-chases to reach the user's limit.
		var sessions []memory.SessionSummary
		cursor := ""
		for len(sessions) < limit {
			pageSize := limit - len(sessions)
			rows, next, err := db.ListSessionsPage(memory.SessionListFilter{
				CWD: cwd, Cursor: cursor, Limit: pageSize,
			})
			if err != nil {
				return fmt.Errorf("list sessions: %w", err)
			}
			sessions = append(sessions, rows...)
			if next == "" {
				break
			}
			cursor = next
		}

		if len(sessions) == 0 {
			cmd.Println("No sessions found.")
			return nil
		}
		for _, s := range sessions {
			title := s.Title
			if title == "" {
				title = "(untitled)"
			}
			// Titles derive from user messages: strip control runes
			// before they reach the terminal (same rule as the picker).
			cmd.Printf("  %s  %s\n", Bold(safeLabel(title)), Dim(s.ID))
			state := "open"
			if s.EndedAt > 0 {
				state = "ended"
			}
			cmd.Printf("        %s\n", Dim(fmt.Sprintf("%s | %s | %d msg(s) | cwd: %s",
				time.Unix(s.UpdatedAt, 0).Format("2006-01-02 15:04"), state, s.MessageCount, safeLabel(s.CWD))))
		}
		return nil
	},
}

// sessionsDeleteCmd removes a session and its messages from the store —
// the same operation session/delete performs for ACP clients.
var sessionsDeleteCmd = &cobra.Command{
	Use:   "delete <sessionId>",
	Short: "Delete a stored conversation session",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		db, err := memory.OpenDefault()
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer db.Close()
		if err := db.DeleteSession(id); err != nil {
			if errors.Is(err, memory.ErrSessionNotFound) {
				return fmt.Errorf("no session %q in the store", id)
			}
			return fmt.Errorf("delete session: %w", err)
		}
		cmd.Printf("Deleted session: %s\n", id)
		return nil
	},
}

func init() {
	sessionsListCmd.Flags().Int("limit", 20, "maximum number of sessions to show")
	sessionsListCmd.Flags().String("cwd", "", "only sessions started in this exact directory")
	sessionsCmd.AddCommand(sessionsListCmd)
	sessionsCmd.AddCommand(sessionsDeleteCmd)
	rootCmd.AddCommand(sessionsCmd)
}
