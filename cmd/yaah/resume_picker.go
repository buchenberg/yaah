package yaah

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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

// normalizeResumeArgs splices the legacy space-separated form
// `--resume <id>` into `--resume=<id>` before flag parsing. pflag's
// value-optional flag (--resume carries a NoOptDefVal picker sentinel)
// never consumes the next token, which would leave the id positional:
// root's one-shot prompt would eat it and subcommands with cobra.NoArgs
// (tui, serve, web, acp) would reject the argument outright. Only a
// token shaped like a generated session id (sess-<digits>) is spliced —
// a prompt like "sess-management is broken" stays a prompt. Everything
// after `--` is positional and never touched.
func normalizeResumeArgs(argv []string) []string {
	out := append([]string(nil), argv...)
	for i := 0; i < len(out)-1; i++ {
		if out[i] == "--" {
			break
		}
		if out[i] == "--resume" && sessionIDShape(out[i+1]) {
			out[i] = "--resume=" + out[i+1]
			out = append(out[:i+1], out[i+2:]...)
			break
		}
	}
	return out
}

// sessionIDShape reports whether s looks like a yaah-generated session
// id (`sess-<unixnano>`); see newAgentSessionWithOptions.
func sessionIDShape(s string) bool {
	if !strings.HasPrefix(s, "sess-") {
		return false
	}
	for _, r := range s[len("sess-"):] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// maybeResolveResumePicker turns the bare --resume sentinel into a
// concrete session id, editing the global flag value. Interactive
// surfaces call it before building their session.
func maybeResolveResumePicker(out io.Writer, in io.Reader) error {
	if resumeSessionID != resumePickerSentinel {
		return nil
	}
	rawCwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resume picker: current directory: %w", err)
	}
	sessions, err := listPickerSessions(rawCwd)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		return fmt.Errorf("no sessions to resume for this directory (starting a run creates one)")
	}
	id, err := pickResumeSession(out, in, sessions)
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

// listPickerSessions returns candidate sessions for the picker. The
// canonical cwd filter (what rows carry going forward) comes first; when
// it finds nothing, the raw process cwd is tried so rows written before
// canonicalization — or through a symlinked PWD — still appear.
func listPickerSessions(rawCwd string) ([]memory.SessionSummary, error) {
	filters := []string{rawCwd}
	if canonical, err := acp.CanonicalFilterCwd(rawCwd); err == nil && canonical != rawCwd {
		filters = []string{canonical, rawCwd}
	}
	db, err := memory.OpenDefault()
	if err != nil {
		return nil, fmt.Errorf("resume picker: %w", err)
	}
	defer db.Close()

	for _, filter := range filters {
		var sessions []memory.SessionSummary
		cursor := ""
		for len(sessions) < maxPickerRows {
			rows, next, err := db.ListSessionsPage(memory.SessionListFilter{
				CWD: filter, Cursor: cursor, Limit: maxPickerRows - len(sessions),
			})
			if err != nil {
				return nil, fmt.Errorf("resume picker: %w", err)
			}
			sessions = append(sessions, rows...)
			if next == "" || len(rows) == 0 {
				break
			}
			cursor = next
		}
		if len(sessions) > 0 {
			return sessions, nil
		}
	}
	return nil, nil
}

// pickResumeSession lists candidate sessions and reads a choice. Empty
// input cancels ("" returned).
func pickResumeSession(out io.Writer, in io.Reader, sessions []memory.SessionSummary) (string, error) {
	if len(sessions) == 0 {
		return "", fmt.Errorf("no sessions to resume for this directory (starting a run creates one)")
	}

	fmt.Fprintf(out, "\n  %s\n", Bold("Resume a session"))
	for i, s := range sessions {
		title := s.Title
		if title == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(out, "  %s %s\n", Bold(strconv.Itoa(i+1)), safeLabel(title))
		fmt.Fprintf(out, "     %s\n", Dim(fmt.Sprintf("%s | %d msg(s) | started %s",
			shortAge(time.Unix(s.UpdatedAt, 0)), s.MessageCount,
			time.Unix(s.StartedAt, 0).Format("2006-01-02 15:04"))))
	}
	fmt.Fprint(out, "\nEnter number (empty to start a new session): ")

	choice, err := readPickerLine(in)
	if err != nil {
		return "", fmt.Errorf("resume picker: no input (use --resume=<id> in non-interactive runs)")
	}
	choice = strings.TrimSpace(choice)
	if choice == "" {
		return "", nil
	}
	n, err := strconv.Atoi(choice)
	if err != nil || n < 1 || n > len(sessions) {
		return "", fmt.Errorf("resume picker: %q is not a valid choice (1..%d)", choice, len(sessions))
	}
	return sessions[n-1].ID, nil
}

// readPickerLine reads one line without buffering past the newline.
// bufio.Scanner reads ahead into its buffer and would swallow input the
// REPL or a piped prompt still needs (e.g. `printf '1\nhello\n' |
// yaah --resume`), so this reads byte-wise and stops at the line end.
// A reader stuck on (0, nil) reads trips io.ErrNoProgress instead of
// spinning.
func readPickerLine(in io.Reader) (string, error) {
	var sb strings.Builder
	var b [1]byte
	idle := 0
	for {
		n, err := in.Read(b[:])
		if n > 0 {
			idle = 0
			if b[0] == '\n' {
				return strings.TrimRight(sb.String(), "\r"), nil
			}
			sb.WriteByte(b[0])
			continue
		}
		if err != nil {
			if sb.Len() > 0 {
				return sb.String(), nil
			}
			return "", err
		}
		idle++
		if idle > 100 {
			return "", io.ErrNoProgress
		}
	}
}

// safeLabel strips ANSI/OSC escape sequences and stray control runes
// from untrusted text before it reaches the terminal: session titles
// derive from user messages and may carry sequences pasted from
// elsewhere.
func safeLabel(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			// ESC: consume the sequence it introduces — CSI (… final
			// byte in @..~), OSC (… BEL or ESC \) — or just the byte.
			i++
			switch {
			case i < len(s) && s[i] == '[':
				i++
				for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
					i++
				}
				i++
			case i < len(s) && s[i] == ']':
				i++
				for i < len(s) && s[i] != 0x07 {
					if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
				if i < len(s) && s[i] == 0x07 {
					i++
				}
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size <= 1 {
			i++
			continue
		}
		if !unicode.IsControl(r) {
			sb.WriteRune(r)
		}
		i += size
	}
	return sb.String()
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
