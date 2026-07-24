package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/config"
)

const (
	defaultTruncateMaxLines = 500
	defaultTruncateMaxBytes = 20 * 1024
)

func (l *Loop) truncateToolResult(result string) string {
	maxLines := l.ToolResultMaxLines
	if maxLines <= 0 {
		maxLines = defaultTruncateMaxLines
	}
	maxBytes := l.ToolResultMaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultTruncateMaxBytes
	}

	lines := strings.Split(result, "\n")
	lineCount := len(lines)
	byteCount := len(result)

	if lineCount <= maxLines && byteCount <= maxBytes {
		return result
	}

	lineCapped := lineCount > maxLines
	byteCapped := byteCount > maxBytes

	var cutIdx int
	var truncated string
	var truncatedLines int

	if lineCapped && (!byteCapped || findLineCutBytePos(result, maxLines) <= maxBytes) {
		cutIdx = findLineCutBytePos(result, maxLines)
		truncated = result[:cutIdx]
		truncatedLines = maxLines
	} else {
		cutIdx = maxBytes
		if cutIdx < len(result) {
			lastNL := strings.LastIndexByte(result[:cutIdx], '\n')
			if lastNL > 0 {
				cutIdx = lastNL
			}
		}
		truncated = result[:cutIdx]
		truncatedLines = strings.Count(truncated, "\n") + 1
	}

	spillDir := filepath.Join(config.HomeDir(), "truncated")
	if err := os.MkdirAll(spillDir, 0o755); err == nil {
		ts := time.Now().UnixNano()
		filename := fmt.Sprintf("%d-tool.txt", ts)
		fullPath := filepath.Join(spillDir, filename)
		if writeErr := os.WriteFile(fullPath, []byte(result), 0o644); writeErr == nil {
			cleanTruncatedDir(spillDir)
			hint := fmt.Sprintf(
				"\n...[output truncated at %d lines / %d bytes — full output at %s. To read portions, use the read tool with offset/limit or spawn a subagent for large analysis.]...",
				truncatedLines, len(result), fullPath,
			)
			return truncated + hint
		}
	}

	return truncated + "\n...[truncated]..."
}

func findLineCutBytePos(s string, maxLines int) int {
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

func cleanTruncatedDir(dir string) {
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
