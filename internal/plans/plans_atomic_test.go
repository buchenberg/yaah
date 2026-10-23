package plans

import (
	"os"
	"path/filepath"
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
