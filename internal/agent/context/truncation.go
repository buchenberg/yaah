package context

import (
	"os"
	"path/filepath"
	"strings"
	"time"
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
