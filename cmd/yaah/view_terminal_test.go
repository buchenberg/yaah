package yaah

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestFirstLineKeepsValidUTF8 pins the status-row truncation: cutting a
// multi-byte rune at the byte bound must not emit invalid UTF-8.
func TestFirstLineKeepsValidUTF8(t *testing.T) {
	line := strings.Repeat("é", 120) // 240 bytes, 2 bytes per rune
	got := firstLine(line)
	if !utf8.ValidString(got) {
		t.Fatalf("firstLine produced invalid UTF-8: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("firstLine did not mark the truncation: %q", got)
	}
}
