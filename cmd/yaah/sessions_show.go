package yaah

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/memory"
	"github.com/buchenberg/yaah/internal/sessions"
	"github.com/spf13/cobra"
)

// sessionsShowCmd prints one stored conversation: metadata header plus the
// message transcript in order. It closes the gap that made 'yaah sessions'
// able to list and delete sessions but not read one — the only ways to see a
// transcript were to resume it in the REPL or to read tool-call traces
// through 'yaah shepherd-trace show', neither of which is inspection.
//
// Like every other surface it goes through the registry (Registry.Transcript)
// rather than opening the database directly.
var sessionsShowCmd = &cobra.Command{
	Use:   "show <sessionId>",
	Short: "Print one session's metadata and message transcript",
	Long: `Print one stored session: metadata header, then every message in
conversation order.

Use 'yaah sessions list' to find the id. Long messages are truncated to
--max-chars (0 disables truncation); pass --no-tools to hide tool traffic and
--json for the raw records.`,
	Example: `  yaah sessions show sess-8f21c0
  yaah sessions show sess-8f21c0 --no-tools
  yaah sessions show sess-8f21c0 --max-chars 0`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]

		reg, err := sessions.OpenDefault()
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer reg.Close()

		sess, msgs, err := reg.Transcript(id)
		if err != nil {
			if errors.Is(err, sessions.ErrNotFound) {
				return fmt.Errorf("no such session: %s", id)
			}
			return fmt.Errorf("read session: %w", err)
		}

		jsonOut, _ := cmd.Flags().GetBool("json")
		noTools, _ := cmd.Flags().GetBool("no-tools")
		maxChars, _ := cmd.Flags().GetInt("max-chars")

		if jsonOut {
			return printJSON(cmd, map[string]any{"session": sess, "messages": msgs})
		}

		state := "open"
		if sess.EndedAt > 0 {
			state = "ended"
		}
		cmd.Printf("%s\n", Bold(safeLabel(sess.ID)))
		cmd.Printf("  %s\n", Dim(fmt.Sprintf("%s | %s | %d msg(s)", time.Unix(sess.StartedAt, 0).Format("2006-01-02 15:04"), state, len(msgs))))
		if sess.CWD != "" {
			cmd.Printf("  %s\n", Dim("cwd: "+safeLabel(sess.CWD)))
		}
		if sess.Model != "" {
			cmd.Printf("  %s\n", Dim("model: "+safeLabel(sess.Model)))
		}
		if sess.TokensIn+sess.TokensOut > 0 {
			cmd.Printf("  %s\n", Dim(fmt.Sprintf("tokens: %d in / %d out", sess.TokensIn, sess.TokensOut)))
		}
		cmd.Println()

		shown := 0
		for _, m := range msgs {
			if noTools && isToolTraffic(m) {
				continue
			}
			cmd.Printf("%s\n", renderTranscriptMessage(m, maxChars))
			shown++
		}
		if shown == 0 {
			cmd.Println(Dim("(no messages)"))
		}
		return nil
	},
}

// printJSON writes v as indented JSON to the command's stdout, so `--json`
// output stays pipeable (no color, no human formatting).
func printJSON(cmd *cobra.Command, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	cmd.Println(string(data))
	return nil
}

// isToolTraffic reports whether a message is tool plumbing rather than
// conversation: assistant tool calls and tool results.
func isToolTraffic(m memory.Message) bool {
	return m.Role == "tool" || m.ToolCalls != "" || m.ToolName != ""
}

// renderTranscriptMessage formats one message as
//
//	[0007] assistant
//	  the content, indented
func renderTranscriptMessage(m memory.Message, maxChars int) string {
	role := m.Role
	if role == "" {
		role = "unknown"
	}
	label := fmt.Sprintf("[%04d] %s", m.Idx, role)
	if m.ToolName != "" {
		label += " (" + safeLabel(m.ToolName) + ")"
	}
	var b strings.Builder
	b.WriteString(Bold(label))
	b.WriteString("\n")

	content := m.Content
	if content == "" && m.ToolCalls != "" {
		content = m.ToolCalls
	}
	content = truncateChars(safeLabel(content), maxChars)
	for _, line := range strings.Split(content, "\n") {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// truncateChars caps s at maxChars runes, appending a marker. maxChars <= 0
// disables truncation.
func truncateChars(s string, maxChars int) string {
	if maxChars <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxChars {
		return s
	}
	return string(runes[:maxChars]) + fmt.Sprintf(" … (%d more chars)", len(runes)-maxChars)
}

func init() {
	sessionsCmd.AddCommand(sessionsShowCmd)
	sessionsShowCmd.Flags().Bool("json", false, "emit the session and messages as JSON")
	sessionsShowCmd.Flags().Bool("no-tools", false, "hide tool calls and tool results")
	sessionsShowCmd.Flags().Int("max-chars", 2000, "truncate each message to this many characters (0 = no limit)")
}
