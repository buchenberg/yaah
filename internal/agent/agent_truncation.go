package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentctx "github.com/buchenberg/yaah/internal/agent/context"
)

// Re-exports for backward compatibility within the agent package (tests
// reference the historical unexported names).
const defaultTruncateMaxLines = agentctx.DefaultTruncateMaxLines

func cleanTruncatedDir(dir string) { agentctx.CleanTruncatedDir(dir) }

// truncateToolResult applies the loop's outer context-safety cap to a
// SUCCESSFUL tool result. Tool-level truncation and the never-truncate-errors
// invariant live in internal/tools/result.go: tools put failure detail first,
// so this cap can only ever cut success detail. Errors never reach this
// function — agent_dispatch delivers them verbatim.
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

	lineCutPos := agentctx.FindLineCutBytePos(result, maxLines)
	if lineCapped && (!byteCapped || lineCutPos <= maxBytes) {
		cutIdx = lineCutPos
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

	// Spill directory is injected via LoopConfig.ToolSpillDir by the
	// composition root; empty disables spilling (finding C4).
	spillDir := l.Config.ToolSpillDir
	if err := os.MkdirAll(spillDir, 0o755); err == nil {
		ts := time.Now().UnixNano()
		filename := fmt.Sprintf("%d-tool.txt", ts)
		fullPath := filepath.Join(spillDir, filename)
		// A single-line blob (a delegate's JSON envelope, minified output)
		// defeats the recovery hint: the read tool cannot offset into a
		// one-line file, so the spill is hard-wrapped for paging
		// (yaah-was9.14). Content with real line structure spills verbatim.
		spill := result
		wrappedNote := ""
		if strings.Count(result, "\n") <= 1 && len(result) > maxBytes {
			spill = agentctx.WrapForPaging(result, 200)
			wrappedNote = " (line-wrapped for paging — join lines to recover the original bytes)"
		}
		if writeErr := os.WriteFile(fullPath, []byte(spill), 0o644); writeErr == nil {
			agentctx.CleanTruncatedDir(spillDir)
			hint := fmt.Sprintf(
				"\n...[output truncated at %d lines / %d bytes — full output at %s%s. To read portions, use the read tool with offset/limit or spawn a subagent for large analysis.]...",
				truncatedLines, len(result), fullPath, wrappedNote,
			)
			return truncated + hint
		}
	}

	return truncated + "\n...[truncated]..."
}
