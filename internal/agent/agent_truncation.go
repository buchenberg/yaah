package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentctx "github.com/buchenberg/yaah/internal/agent/context"
	"github.com/buchenberg/yaah/internal/config"
)

// Re-exports for backward compatibility within the agent package (tests
// reference the historical unexported names).
const defaultTruncateMaxLines = agentctx.DefaultTruncateMaxLines

var cleanTruncatedDir = agentctx.CleanTruncatedDir

func (l *Loop) truncateToolResult(result string) string {
	maxLines := l.ctxMgr().ToolResultMaxLines
	if maxLines <= 0 {
		maxLines = agentctx.DefaultTruncateMaxLines
	}
	maxBytes := l.ctxMgr().ToolResultMaxBytes
	if maxBytes <= 0 {
		maxBytes = agentctx.DefaultTruncateMaxBytes
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

	if lineCapped && (!byteCapped || agentctx.FindLineCutBytePos(result, maxLines) <= maxBytes) {
		cutIdx = agentctx.FindLineCutBytePos(result, maxLines)
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
			agentctx.CleanTruncatedDir(spillDir)
			hint := fmt.Sprintf(
				"\n...[output truncated at %d lines / %d bytes — full output at %s. To read portions, use the read tool with offset/limit or spawn a subagent for large analysis.]...",
				truncatedLines, len(result), fullPath,
			)
			return truncated + hint
		}
	}

	return truncated + "\n...[truncated]..."
}
