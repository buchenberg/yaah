package plans

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// TestCreateIsExclusive pins yaah-34b: Create used to Stat and then Write, so
// two callers could both observe the plan as absent and the second silently
// destroyed the first's PLAN.md.
func TestCreateIsExclusive(t *testing.T) {
	dir := t.TempDir()

	if _, err := Create(dir, "race", "first", "body one"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := Create(dir, "race", "second", "body two"); err == nil {
		t.Fatal("second create succeeded; want an already-exists error")
	}
	if _, err := Create(dir, "race", "second", "body two"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second create error = %v; want it to mention already exists", err)
	}

	// The original must be intact: a losing create is not allowed to truncate.
	data, err := os.ReadFile(filepath.Join(dir, "race", "PLAN.md"))
	if err != nil {
		t.Fatalf("read PLAN.md: %v", err)
	}
	if !strings.Contains(string(data), "first") || !strings.Contains(string(data), "body one") {
		t.Fatalf("PLAN.md was overwritten: %s", data)
	}
}

// TestCreateConcurrentCreatesOnlyOneWins is the same race expressed as
// contention: exactly one winner, and the file matches that winner.
func TestCreateConcurrentCreatesOnlyOneWins(t *testing.T) {
	dir := t.TempDir()

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	bodies := make([]string, n)
	for i := 0; i < n; i++ {
		bodies[i] = "body-" + string(rune('a'+i))
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = Create(dir, "contended", "desc", bodies[i])
		}(i)
	}
	wg.Wait()

	winners := 0
	winner := -1
	for i, err := range errs {
		switch {
		case err == nil:
			winners++
			winner = i
		case strings.Contains(err.Error(), "already exists"):
			// expected for the losers
		default:
			t.Fatalf("create %d: unexpected error %v", i, err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d; want exactly 1", winners)
	}

	data, err := os.ReadFile(filepath.Join(dir, "contended", "PLAN.md"))
	if err != nil {
		t.Fatalf("read PLAN.md: %v", err)
	}
	if !strings.Contains(string(data), bodies[winner]) {
		t.Fatalf("PLAN.md holds %q, want the winner's body %q", data, bodies[winner])
	}
}

// TestEditLeavesNoTempFiles checks the atomic replace cleans up after itself:
// a stray .PLAN-*.tmp in a plan directory would be picked up by nothing, but it
// would accumulate on every status change.
func TestEditLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := Create(dir, "cleanup", "desc", "body"); err != nil {
		t.Fatalf("create: %v", err)
	}
	got := &Plan{Name: "cleanup", Path: filepath.Join(dir, "cleanup", "PLAN.md")}
	if _, err := SetStatus(got, "in_progress"); err != nil {
		t.Fatalf("set status: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "cleanup"))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".PLAN-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

// TestEditPreservesFileMode pins the review finding that the atomic replace
// used a fixed 0644: a plan created (or chmod-ed) 0600 must not be silently
// widened by a status change. Skipped on Windows, where file modes carry no
// permission bits.
func TestEditPreservesFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not meaningful on Windows")
	}
	dir := t.TempDir()
	if _, err := Create(dir, "secret", "desc", "body"); err != nil {
		t.Fatalf("create: %v", err)
	}
	path := filepath.Join(dir, "secret", "PLAN.md")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	plan := &Plan{Name: "secret", Path: path}
	if _, err := SetStatus(plan, "in_progress"); err != nil {
		t.Fatalf("set status: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o; want 600 (a status change must not widen it)", got)
	}
}

// TestEditPreservesUnmodelledFrontmatter pins yaah-ayh: Edit rebuilt the
// frontmatter from name/description/status alone, so every other key — owner,
// a linked issue, a dependency — was silently deleted on each status change.
func TestEditPreservesUnmodelledFrontmatter(t *testing.T) {
	dir := t.TempDir()
	planDir := filepath.Join(dir, "keepme")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(planDir, "PLAN.md")
	original := "---\n" +
		"name: keepme\n" +
		"description: keep my extras\n" +
		"status: draft\n" +
		"# managed by the issue tracker\n" +
		"owner: greg\n" +
		"depends_on:\n  - yaah-34b\n" +
		"issue: https://github.com/buchenberg/yaah/issues/530\n" +
		"---\n\nBody text.\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := SetStatus(&Plan{Name: "keepme", Path: path}, "in_progress"); err != nil {
		t.Fatalf("set status: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)

	for _, want := range []string{
		"status: in_progress",            // the modelled key was updated
		"# managed by the issue tracker", // comment survived
		"owner: greg",                    // unknown scalar survived
		"depends_on:",                    // unknown nested key survived
		"  - yaah-34b",                   // and its sequence item
		"issue: https://github.com/buchenberg/yaah/issues/530",
		"Body text.", // body untouched
	} {
		if !strings.Contains(got, want) {
			t.Errorf("PLAN.md missing %q\n---\n%s", want, got)
		}
	}

	// And the result must still parse, with the modelled fields correct.
	plan, err := ParseFrontmatter(data)
	if err != nil {
		t.Fatalf("parse after edit: %v", err)
	}
	if plan.Status != "in_progress" {
		t.Errorf("status = %q; want in_progress", plan.Status)
	}
	if plan.Name != "keepme" {
		t.Errorf("name = %q; want keepme", plan.Name)
	}
	if plan.Description != "keep my extras" {
		t.Errorf("description = %q; want 'keep my extras'", plan.Description)
	}
}

// TestEditHandlesCRLFFrontmatter pins the review finding that a CRLF file had
// its modelled keys appended as duplicates instead of replaced: the trailing
// "\r" defeated the column-zero test.
func TestEditHandlesCRLFFrontmatter(t *testing.T) {
	dir := t.TempDir()
	planDir := filepath.Join(dir, "crlf")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(planDir, "PLAN.md")
	crlf := "---\r\nname: crlf\r\ndescription: has crlf\r\nstatus: draft\r\n---\r\n\r\nBody.\r\n"
	if err := os.WriteFile(path, []byte(crlf), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := SetStatus(&Plan{Name: "crlf", Path: path}, "approved"); err != nil {
		t.Fatalf("set status: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	if n := strings.Count(got, "status:"); n != 1 {
		t.Fatalf("status key appears %d times; want 1 (duplicated, not replaced)\n%s", n, got)
	}
	if !strings.Contains(got, "status: approved") {
		t.Fatalf("status not updated:\n%s", got)
	}
	plan, err := ParseFrontmatter(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if plan.Status != "approved" {
		t.Errorf("status = %q; want approved", plan.Status)
	}
}

// TestEditKeepsInlineCommentAndQuoting pins the second review finding: a naive
// line rewrite dropped a trailing inline comment and silently unquoted a
// quoted scalar.
func TestEditKeepsInlineCommentAndQuoting(t *testing.T) {
	dir := t.TempDir()
	planDir := filepath.Join(dir, "styled")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(planDir, "PLAN.md")
	original := "---\n" +
		"name: \"styled\"\n" +
		"description: styled plan\n" +
		"status: draft # set by the release bot\n" +
		"---\n\nBody.\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := SetStatus(&Plan{Name: "styled", Path: path}, "completed"); err != nil {
		t.Fatalf("set status: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "status: completed # set by the release bot") {
		t.Errorf("inline comment lost or status not updated:\n%s", got)
	}
	if !strings.Contains(got, `name: "styled"`) {
		t.Errorf("original quoting was dropped:\n%s", got)
	}
}

// TestEditDoesNotDuplicateInlineComments pins the follow-on review finding: the
// parser kept an inline comment as part of the modelled value, so Edit re-emitted
// it inside the value and then again as the preserved comment.
func TestEditDoesNotDuplicateInlineComments(t *testing.T) {
	dir := t.TempDir()
	planDir := filepath.Join(dir, "dup")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(planDir, "PLAN.md")
	original := "---\n" +
		"name: dup\n" +
		"description: keep me # tracked by the bot\n" +
		"status: draft\n" +
		"---\n\nBody.\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Touch only the status: description is carried through unchanged.
	if _, err := SetStatus(&Plan{Name: "dup", Path: path}, "approved"); err != nil {
		t.Fatalf("set status: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(data)
	if n := strings.Count(got, "tracked by the bot"); n != 1 {
		t.Fatalf("inline comment appears %d times; want 1\n%s", n, got)
	}
	if !strings.Contains(got, "description: keep me # tracked by the bot") {
		t.Fatalf("description line mangled:\n%s", got)
	}

	// The parsed description must not carry the comment either.
	plan, err := ParseFrontmatter(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if plan.Description != "keep me" {
		t.Errorf("description = %q; want %q", plan.Description, "keep me")
	}
}
