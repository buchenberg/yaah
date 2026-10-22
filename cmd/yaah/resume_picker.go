package yaah

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/acp"
	"github.com/buchenberg/yaah/internal/memory"
)

// The interactive --resume picker. `yaah --resume` (no value) resolves
// the sentinel below into a concrete session id by listing the store's
// sessions for the current directory, the same canonical-filtered page
// the CLI and ACP session/list read. Non-interactive surfaces (web,
// serve, ACP) never invoke it.

// resumePickerSentinel is the --resume flag value meaning "pick from a
// list". Set as the flag's NoOptDefVal so a bare --resume works.
const resumePickerSentinel = "picker"

// maxPickerRows caps the sessions a picker offers before pagination
// would be required; a picker is a chooser, not a browser.
const maxPickerRows = 40

// maybeResolveResumePicker turns the bare --resume sentinel into a
// concrete session id, editing the global flag value. Interactive
// surfaces call it before building their session.
func maybeResolveResumePicker(out io.Writer, in io.Reader) error {
	if resumeSessionID != resumePickerSentinel {
		return nil
	}
	cwd, err := os.Getwd()
	if err == nil {
		cwd, err = acp.CanonicalFilterCwd(cwd)
	}
	if err != nil {
		return fmt.Errorf("resume picker: current directory: %v", err)
	}
	id, err := pickResumeSession(out, in, cwd)
	if err != nil {
		return err
	}
	if id == "" {
		// No selection: start fresh instead of resuming.
		resumeSessionID = ""
		return nil
	}
	resumeSessionID = id
	return nil
}

// pickResumeSession lists candidate sessions and reads a choice. Empty
// input cancels ("" returned); rows are the current-dwd filtered pages
// like the yaah sessions CLI.
func pickResumeSession(out io.Writer, in io.Reader, cwdFilter string) (string, error) {
	db, err := memory.OpenDefault()
	if err != nil {
		return "", fmt.Errorf("resume picker: %v", err)
	}
	defer db.Close()

	var sessions []memory.SessionSummary
	cursor := ""
	for len(sessions) < maxPickerRows {
		pageSize := maxPickerRows - len(sessions)
		rows, next, err := db.ListSessionsPage(memory.SessionListFilter{
			CWD: cwdFilter, Cursor: cursor, Limit: pageSize,
		})
		if err != nil {
			return "", fmt.Errorf("resume picker: %v", err)
		}
		sessions = append(sessions, rows...)
		if next == "" || len(rows) == 0 {
			break
		}
		cursor = next
	}
	if len(sessions) == 0 {
		return "", fmt.Errorf("no sessions to resume for this directory (starting a run creates one)")
	}

	fmt.Fprintf(out, "\n  %s\n", Bold("Resume a session"))
	for i, s := range sessions {
		title := s.Title
		if title == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(out, "  %s %s\n", Bold(strconv.Itoa(i+1)), title)
		fmt.Fprintf(out, "     %s\n", Dim(fmt.Sprintf("%s | %d msg(s) | started %s",
			shortAge(time.Unix(s.UpdatedAt, 0)), s.MessageCount,
			time.Unix(s.StartedAt, 0).Format("2006-01-02 15:04"))))
	}
	fmt.Fprint(out, "\nEnter number (empty to start a new session): ")

	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		return "", fmt.Errorf("resume picker: no input (use --resume=<id> in non-interactive runs)")
	}
	choice := strings.TrimSpace(scanner.Text())
	if choice == "" {
		return "", nil
	}
	n, err := strconv.Atoi(choice)
	if err != nil || n < 1 || n > len(sessions) {
		return "", fmt.Errorf("resume picker: %q is not a valid choice (1..%d)", choice, len(sessions))
	}
	return sessions[n-1].ID, nil
}

// shortAge renders an approximate age for the picker's dim line.
func shortAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours())/24)
	default:
		return t.Format("Jan 2")
	}
}
