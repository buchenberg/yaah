package yaah

import (
	"errors"
	"fmt"
	"time"

	"github.com/buchenberg/yaah/internal/cwdpath"
	"github.com/buchenberg/yaah/internal/sessions"
	"github.com/spf13/cobra"
)

// sessionsCmd is the `yaah sessions` subcommand tree: direct access to
// the persisted conversation store from the CLI. Every surface reads the
// same rows through the engine-level session registry
// (internal/sessions): ACP session/list and session/delete, the web
// session picker, the interactive resume picker, and this command all
// page, filter, and delete through it.
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
		// Canonicalize before opening the store: a rejected --cwd must not
		// leave a freshly created state.db behind on a cold machine. The
		// canonical value is what the filter then carries, so the registry's
		// own pass (List still performs one) returns the same spelling rather
		// than a differently-spelled equivalent. That pass is idempotent, not
		// skipped — it stays because the ACP and picker callers rely on the
		// registry to canonicalize for them.
		cwd, err := cwdpath.Canonical(cwdFlag)
		if err != nil {
			return fmt.Errorf("--cwd: %v", err)
		}

		reg, err := sessions.OpenDefault()
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer reg.Close()

		// The registry canonicalizes the cwd filter exactly like the ACP
		// session/list intake, so the CLI and ACP filters match the stored
		// session cwds identically, and it chases the store's cursors up to
		// the user's limit.
		rows, err := reg.List(sessions.Filter{CWD: cwd, Limit: limit})
		if err != nil {
			return fmt.Errorf("list sessions: %w", err)
		}

		if len(rows) == 0 {
			cmd.Println("No sessions found.")
			return nil
		}
		for _, s := range rows {
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
		reg, err := sessions.OpenDefault()
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer reg.Close()
		if err := reg.Delete(id); err != nil {
			if errors.Is(err, sessions.ErrNotFound) {
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
