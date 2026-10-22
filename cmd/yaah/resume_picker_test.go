package yaah

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/acp"
	"github.com/buchenberg/yaah/internal/memory"
)

// pickerFixture opens a temp default store (YAAH_HOME redirect) with two
// sessions in the named cwd, the newest one titled. The cwd is the
// symlink-resolved directory (the same canonicalization the picker
// applies to os.Getwd) so the interactive path matches the store rows on
// every platform (/tmp → /private/tmp on macOS). Returns the resolved
// cwd; chdirDir is the real directory for t.Chdir.
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

func TestPickResumeSession_selection(t *testing.T) {
	cwd, _ := pickerFixture(t)

	var out bytes.Buffer
	id, err := pickResumeSession(&out, strings.NewReader("1\n"), cwd)
	if err != nil {
		t.Fatal(err)
	}
	if id != "sess-new" {
		t.Fatalf("picked %q, want the newest (sess-new)", id)
	}
	if !strings.Contains(out.String(), "fix the flaky test") || !strings.Contains(out.String(), "older untitled work") {
		t.Fatalf("picker output missing titles:\n%s", out.String())
	}
}

func TestPickResumeSession_numberingIsNewestFirst(t *testing.T) {
	cwd, _ := pickerFixture(t)

	var out bytes.Buffer
	id, err := pickResumeSession(&out, strings.NewReader("2\n"), cwd)
	if err != nil {
		t.Fatal(err)
	}
	if id != "sess-old" {
		t.Fatalf("choice 2 = %q, want sess-old (keyset pages run newest first)", id)
	}
}

func TestPickResumeSession_emptyInputStartsFresh(t *testing.T) {
	cwd, _ := pickerFixture(t)

	id, err := pickResumeSession(&bytes.Buffer{}, strings.NewReader("\n"), cwd)
	if err != nil || id != "" {
		t.Fatalf("empty input = (%q, %v), want start-fresh (\"\", nil)", id, err)
	}
}

func TestPickResumeSession_badInput(t *testing.T) {
	cwd, _ := pickerFixture(t)

	for name, input := range map[string]string{
		"out of range": "9\n",
		"not a number": "two\n",
		"eof":          "",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := pickResumeSession(&bytes.Buffer{}, strings.NewReader(input), cwd); err == nil {
				t.Fatal("invalid picker input must error")
			}
		})
	}
}

func TestPickResumeSession_emptyStore(t *testing.T) {
	t.Setenv("YAAH_HOME", t.TempDir())
	const cwd = "/tmp/empty-project"
	// OpenDefault once so the store exists, then close it: the picker
	// must then report no candidates rather than crash.
	db, err := memory.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	if _, err := pickResumeSession(&bytes.Buffer{}, strings.NewReader("1\n"), cwd); err == nil || !strings.Contains(err.Error(), "no sessions to resume") {
		t.Fatalf("err = %v, want \"no sessions to resume\"", err)
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
