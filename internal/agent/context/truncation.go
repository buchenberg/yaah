package context

import (
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// DefaultTruncateMaxLines is the default line cap for tool results.
	DefaultTruncateMaxLines = 500
	// DefaultTruncateMaxBytes is the default byte cap for tool results.
	DefaultTruncateMaxBytes = 20 * 1024
)

// FindLineCutBytePos returns the byte offset in s immediately after the
// maxLines-th newline, or len(s) if s contains fewer lines.
func FindLineCutBytePos(s string, maxLines int) int {
	for i, n := 0, 0; i < len(s); {
		if n >= maxLines {
			return i
		}
		nl := strings.IndexByte(s[i:], '\n')
		if nl < 0 {
			return len(s)
		}
		i += nl + 1
		n++
	}
	return len(s)
}

// WrapForPaging hard-wraps s at width-byte lines (backing off to rune
// boundaries, byte-preserving for invalid UTF-8) so a single-line blob — a
// delegate's JSON envelope, minified output — becomes pageable by line-based
// readers; the spill file's own recovery hint is a dead end when the read
// tool cannot offset into a one-line file (yaah-was9.14). Removing the
// inserted newlines reconstructs s exactly when s contained none.
func WrapForPaging(s string, width int) string {
	if width <= 0 || len(s) <= width {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + len(s)/width + 1)
	for len(s) > width {
		cut := width
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut == 0 {
			cut = width // no rune start within width; cut hard rather than spin
		}
		b.WriteString(s[:cut])
		b.WriteByte('\n')
		s = s[cut:]
	}
	if s != "" {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	return b.String()
}

// CleanTruncatedDir removes spill files older than 7 days from dir.
func CleanTruncatedDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
