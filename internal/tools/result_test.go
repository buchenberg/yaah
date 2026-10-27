package tools

// result_test.go pins the tool-result policy in result.go: one truncation
// owner, UTF-8-safe cuts, and multiResult's two guarantees — failures are
// rendered before success detail and a partial application is an error.

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateResult(t *testing.T) {
	t.Run("short result unchanged", func(t *testing.T) {
		if got := truncateResult("short"); got != "short" {
			t.Errorf("truncateResult(short) = %q", got)
		}
	})

	t.Run("long result capped and marked", func(t *testing.T) {
		in := strings.Repeat("x", toolResultMaxBytes*2)
		got := truncateResult(in)
		if !strings.HasSuffix(got, truncationMarker) {
			t.Errorf("capped result missing marker: %q", got[len(got)-40:])
		}
		if len(got) >= len(in) {
			t.Errorf("capped result is not shorter: %d >= %d", len(got), len(in))
		}
	})

	t.Run("cut lands on a rune boundary", func(t *testing.T) {
		// Fill up to just before the cap, then put a 3-byte rune across it.
		prefix := strings.Repeat("a", toolResultMaxBytes-1)
		got := truncateResult(prefix + "€" + "tail")
		if !utf8.ValidString(got) {
			t.Fatalf("truncateResult produced invalid UTF-8: %q", got)
		}
		if !strings.HasSuffix(got, truncationMarker) {
			t.Errorf("missing marker: %q", got)
		}
	})
}

func TestTruncateOutput(t *testing.T) {
	short := []byte("ok")
	if got := truncateOutput(short); string(got) != "ok" {
		t.Errorf("short output changed: %q", got)
	}

	long := []byte(strings.Repeat("y", commandOutputMaxBytes+10))
	got := truncateOutput(long)
	if !strings.HasSuffix(string(got), truncationMarker) {
		t.Error("capped command output missing marker")
	}
}

func TestMultiResult_SuccessHasNoError(t *testing.T) {
	var r multiResult
	r.donef("file a: 2 match(es)")
	r.donef("file b: 1 match(es)")

	report, err := r.Report("Replaced 3 match(es) across 2 file(s):")
	if err != nil {
		t.Fatalf("Report() error = %v, want nil", err)
	}
	if !strings.Contains(report, "file a: 2 match(es)") || !strings.Contains(report, "file b: 1 match(es)") {
		t.Errorf("report lost success detail: %q", report)
	}
}

// TestMultiResult_FailureIsAnErrorAndIsNeverTruncated is the class fix: a
// partial application returns a non-nil error, and a report far larger than
// the cap still carries every failure line intact.
func TestMultiResult_FailureIsAnErrorAndIsNeverTruncated(t *testing.T) {
	var r multiResult
	for i := 0; i < 500; i++ {
		r.donef("success line %d: %s", i, strings.Repeat("detail ", 8))
	}
	r.failf("file a: permission denied")
	r.failf("file b: no such file")

	report, err := r.Report("Replaced 500 match(es) across 500 file(s):")
	if err == nil {
		t.Fatal("Report() error = nil, want non-nil for a partial application")
	}
	if err.Error() != report {
		t.Error("error text must carry the same report so no detail is lost on the error path")
	}
	if !strings.Contains(report, "file a: permission denied") || !strings.Contains(report, "file b: no such file") {
		t.Fatalf("failure detail was truncated or dropped:\n%s", report)
	}
	if strings.Index(report, "file a: permission denied") > strings.Index(report, "success line 0") {
		t.Error("failures must be rendered before success detail so a cap can only eat success")
	}
	if !strings.Contains(report, truncationMarker) {
		t.Error("expected the success detail to be capped in this scenario")
	}
	if strings.Count(report, "FAILED (2):") != 1 {
		t.Errorf("missing failure banner: %q", report)
	}
}
