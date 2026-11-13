package context

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWrapForPaging_ShortInputUntouched(t *testing.T) {
	if got := WrapForPaging("short", 200); got != "short" {
		t.Errorf("short input modified: %q", got)
	}
	if got := WrapForPaging("exact", 5); got != "exact" {
		t.Errorf("input at width modified: %q", got)
	}
	if got := WrapForPaging("anything", 0); got != "anything" {
		t.Errorf("non-positive width must disable wrapping: %q", got)
	}
}

func TestWrapForPaging_SingleLineReconstructable(t *testing.T) {
	orig := `{"result":"` + strings.Repeat("report ", 200) + `"}`
	wrapped := WrapForPaging(orig, 200)

	if strings.Count(wrapped, "\n") < 2 {
		t.Fatalf("wrapped output should have multiple lines, got %d", strings.Count(wrapped, "\n"))
	}
	for _, line := range strings.Split(strings.TrimSuffix(wrapped, "\n"), "\n") {
		if len(line) > 200 {
			t.Errorf("line exceeds width: %d bytes", len(line))
		}
	}
	if joined := strings.ReplaceAll(wrapped, "\n", ""); joined != orig {
		t.Error("joining wrapped lines must reconstruct the original bytes exactly")
	}
}

func TestWrapForPaging_RuneBoundariesPreserved(t *testing.T) {
	orig := strings.Repeat("é€", 300) // multi-byte runes throughout
	wrapped := WrapForPaging(orig, 200)

	for i, line := range strings.Split(strings.TrimSuffix(wrapped, "\n"), "\n") {
		if !utf8.ValidString(line) {
			t.Errorf("line %d is not valid UTF-8 — a rune was split", i)
		}
	}
	if joined := strings.ReplaceAll(wrapped, "\n", ""); joined != orig {
		t.Error("wrapping must be byte-preserving for valid UTF-8")
	}
}

func TestWrapForPaging_InvalidBytesSurvive(t *testing.T) {
	orig := strings.Repeat("\x00\x01\xfe\xff", 100) // includes invalid UTF-8
	wrapped := WrapForPaging(orig, 64)

	if joined := strings.ReplaceAll(wrapped, "\n", ""); joined != orig {
		t.Error("invalid UTF-8 bytes must pass through unchanged (no rune re-encoding)")
	}
}
