package yaah

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/acp"
	"github.com/buchenberg/yaah/internal/memory"
)

// pickerRows is the canned candidate list for the pure picker tests:
// newest first, as listPickerSessions returns them.
func pickerRows() []memory.SessionSummary {
	now := time.Now().Unix()
	return []memory.SessionSummary{
		{ID: "sess-new", Title: "fix the flaky test", StartedAt: now - 60, UpdatedAt: now, MessageCount: 2},
		{ID: "sess-old", Title: "older untitled work", StartedAt: now - 600, UpdatedAt: now - 600, MessageCount: 5},
	}
}

// pickerFixture opens a temp default store (YAAH_HOME redirect) with two
// sessions in the named cwd, the newest one titled. Rows are written the
// way production writes them (canonical cwd — canonicalStoredCwd in
// wiring.go). Returns the resolved cwd and the real directory for
// t.Chdir.
func pickerFixture(t *testing.T) (cwd, chdirDir string) {
	t.Helper()
	t.Setenv("YAAH_HOME", t.TempDir())
	chdirDir = t.TempDir()
	cwd, err := acp.CanonicalFilterCwd(chdirDir)
	if err != nil {
		t.Fatal(err)
	}
	db, err := memory.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Now().Unix()
	if err := db.CreateSession(memory.Session{ID: "sess-old", StartedAt: now - 600, CWD: cwd, Title: "older untitled work"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(memory.Session{ID: "sess-new", StartedAt: now - 60, CWD: cwd, Title: "fix the flaky test"}); err != nil {
		t.Fatal(err)
	}
	return cwd, chdirDir
}

func TestPickResumeSession_selection(t *testing.T) {
	rows := pickerRows()
	var out bytes.Buffer
	id, err := pickResumeSession(&out, strings.NewReader("1\n"), rows)
	if err != nil {
		t.Fatal(err)
	}
	if id != "sess-new" {
		t.Fatalf("picked %q, want the first row (sess-new)", id)
	}
	if !strings.Contains(out.String(), "fix the flaky test") || !strings.Contains(out.String(), "older untitled work") {
		t.Fatalf("picker output missing titles:\n%s", out.String())
	}
}

func TestPickResumeSession_numbering(t *testing.T) {
	id, err := pickResumeSession(&bytes.Buffer{}, strings.NewReader("2\n"), pickerRows())
	if err != nil {
		t.Fatal(err)
	}
	if id != "sess-old" {
		t.Fatalf("choice 2 = %q, want sess-old", id)
	}
}

func TestPickResumeSession_emptyInputStartsFresh(t *testing.T) {
	id, err := pickResumeSession(&bytes.Buffer{}, strings.NewReader("\n"), pickerRows())
	if err != nil || id != "" {
		t.Fatalf("empty input = (%q, %v), want start-fresh (\"\", nil)", id, err)
	}
}

func TestPickResumeSession_badInput(t *testing.T) {
	for name, input := range map[string]string{
		"out of range": "9\n",
		"not a number": "two\n",
		"eof":          "",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := pickResumeSession(&bytes.Buffer{}, strings.NewReader(input), pickerRows()); err == nil {
				t.Fatal("invalid picker input must error")
			}
		})
	}
}

// TestReadPickerLine_noReadAhead pins that consuming the picker's line
// leaves the rest of stdin intact for the REPL or prompt: a buffering
// scanner would swallow "hello" here.
func TestReadPickerLine_noReadAhead(t *testing.T) {
	in := strings.NewReader("1\nhello\n")
	first, err := readPickerLine(in)
	if err != nil || first != "1" {
		t.Fatalf("first line = (%q, %v), want \"1\"", first, err)
	}
	second, err := readPickerLine(in)
	if err != nil || second != "hello" {
		t.Fatalf("second line = (%q, %v), want \"hello\" (input was over-consumed)", second, err)
	}
}

func TestReadPickerLine_finalLineWithoutNewline(t *testing.T) {
	line, err := readPickerLine(strings.NewReader("7"))
	if err != nil || line != "7" {
		t.Fatalf("line = (%q, %v), want \"7\"", line, err)
	}
	if _, err := readPickerLine(strings.NewReader("")); err != io.EOF {
		t.Fatalf("empty input err = %v, want io.EOF", err)
	}
}

func TestSafeLabel(t *testing.T) {
	for name, tc := range map[string][2]string{
		"csi colors":  {"fix \x1b[31mred\x1b[0m text", "fix red text"},
		"osc title":   {"\x1b]0;evil\x07title", "title"},
		"osc with st": {"\x1b]8;;http://x\x1b\\linked", "linked"},
		"c0 bytes":    {"a\x00b\x07c", "abc"},
		"plain":       {"plain text", "plain text"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := safeLabel(tc[0]); got != tc[1] {
				t.Fatalf("safeLabel(%q) = %q, want %q", tc[0], got, tc[1])
			}
		})
	}
}

func TestNormalizeResumeArgs(t *testing.T) {
	for name, tc := range map[string]struct {
		argv []string
		want []string
	}{
		"legacy id before prompt": {[]string{"--resume", "sess-123", "fix tests"}, []string{"--resume=sess-123", "fix tests"}},
		"legacy id for repl":      {[]string{"--resume", "sess-1234567890"}, []string{"--resume=sess-1234567890"}},
		"subcommand surface":      {[]string{"tui", "--resume", "sess-123"}, []string{"tui", "--resume=sess-123"}},
		"prompt with sess- word":  {[]string{"--resume", "sess-management", "is", "broken"}, []string{"--resume", "sess-management", "is", "broken"}},
		"non-numeric id":          {[]string{"--resume", "sess-abc"}, []string{"--resume", "sess-abc"}},
		"explicit equals form":    {[]string{"--resume=sess-123", "hello"}, []string{"--resume=sess-123", "hello"}},
		"bare picker form":        {[]string{"--resume", "hello"}, []string{"--resume", "hello"}},
		"no resume flag":          {[]string{"sessions", "list"}, []string{"sessions", "list"}},
		"after double dash":       {[]string{"--", "--resume", "sess-123"}, []string{"--", "--resume", "sess-123"}},
	} {
		t.Run(name, func(t *testing.T) {
			got := normalizeResumeArgs(tc.argv)
			if len(got) != len(tc.want) {
				t.Fatalf("argv = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("argv = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// stuckReader always reports a legal but progress-free (0, nil) read.
type stuckReader struct{}

func (stuckReader) Read([]byte) (int, error) { return 0, nil }

func TestReadPickerLine_noProgress(t *testing.T) {
	if _, err := readPickerLine(stuckReader{}); err != io.ErrNoProgress {
		t.Fatalf("err = %v, want io.ErrNoProgress (a (0, nil) reader must not spin)", err)
	}
}

func TestListPickerSessions_canonicalRows(t *testing.T) {
	cwd, chdirDir := pickerFixture(t)
	rows, err := listPickerSessions(chdirDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (canonical filter %q)", len(rows), cwd)
	}
}

// TestListPickerSessions_legacyRawCwdRows pins the fallback: rows
// written before cwd canonicalization carry the raw (symlinked) path and
// must still be found through it.
func TestListPickerSessions_legacyRawCwdRows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory symlinks need elevated privileges on Windows")
	}
	t.Setenv("YAAH_HOME", t.TempDir())
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	db, err := memory.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// Legacy rows store the path the process saw, symlink and all.
	if err := db.CreateSession(memory.Session{ID: "sess-legacy", StartedAt: time.Now().Unix(), CWD: link}); err != nil {
		t.Fatal(err)
	}

	rows, err := listPickerSessions(link)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != "sess-legacy" {
		t.Fatalf("rows = %+v, want the legacy row via the raw fallback", rows)
	}
}

func TestMaybeResolveResumePicker(t *testing.T) {
	_, dir := pickerFixture(t)
	t.Chdir(dir)

	orig := resumeSessionID
	t.Cleanup(func() { resumeSessionID = orig })

	// A concrete id passes through untouched.
	resumeSessionID = "sess-explicit"
	if err := maybeResolveResumePicker(&bytes.Buffer{}, strings.NewReader("\n")); err != nil {
		t.Fatal(err)
	}
	if resumeSessionID != "sess-explicit" {
		t.Fatalf("concrete id mangled to %q", resumeSessionID)
	}

	// The sentinel resolves through the picker.
	resumeSessionID = resumePickerSentinel
	if err := maybeResolveResumePicker(&bytes.Buffer{}, strings.NewReader("1\n")); err != nil {
		t.Fatal(err)
	}
	if resumeSessionID != "sess-new" {
		t.Fatalf("sentinel resolved to %q, want sess-new", resumeSessionID)
	}

	// Empty choice cancels back to a fresh session.
	resumeSessionID = resumePickerSentinel
	if err := maybeResolveResumePicker(&bytes.Buffer{}, strings.NewReader("\n")); err != nil {
		t.Fatal(err)
	}
	if resumeSessionID != "" {
		t.Fatalf("cancel left %q, want empty (fresh session)", resumeSessionID)
	}
}

func TestMaybeResolveResumePicker_emptyStore(t *testing.T) {
	_, _ = pickerFixture(t) // rows exist in one directory…
	t.Chdir(t.TempDir())    // …but not in this one

	orig := resumeSessionID
	t.Cleanup(func() { resumeSessionID = orig })
	resumeSessionID = resumePickerSentinel
	if err := maybeResolveResumePicker(&bytes.Buffer{}, strings.NewReader("1\n")); err == nil || !strings.Contains(err.Error(), "no sessions to resume") {
		t.Fatalf("err = %v, want \"no sessions to resume\"", err)
	}
}

func TestMaybeResolveResumePicker_sentinelRejectedNonInteractive(t *testing.T) {
	orig := resumeSessionID
	t.Cleanup(func() { resumeSessionID = orig })
	resumeSessionID = resumePickerSentinel

	// Non-interactive surfaces call newAgentSessionWithOptions directly
	// (no picker in front): the sentinel must fail fast with guidance,
	// before any config or provider work.
	_, err := newAgentSessionWithOptions(SessionOptions{ResumeSessionID: resumePickerSentinel}, true, true)
	if err == nil || !strings.Contains(err.Error(), "--resume without a value") {
		t.Fatalf("err = %v, want sentinel rejection", err)
	}
}
