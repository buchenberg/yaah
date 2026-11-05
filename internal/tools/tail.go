package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
)

const (
	tailDefaultLines = 50
	tailMaxLines     = 10000
	// tailChunkBytes is the backward read size; tailMaxBackBytes bounds how
	// far a line-oriented tail searches for enough newlines.
	tailChunkBytes   = 32 * 1024
	tailMaxBackBytes = 4 * 1024 * 1024
)

// TailTool returns the end of a file without reading the whole thing into
// context. It is the read-side companion to append: follow a large log with
// bounded calls rather than re-reading it.
type TailTool struct {
	PV *PathValidator
	WS Workspace
}

var (
	_ PathValidatorSetter = (*TailTool)(nil)
	_ WorkspaceSetter     = (*TailTool)(nil)
)

func (t *TailTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *TailTool) SetWorkspace(ws Workspace)          { t.WS = ws }

func (t *TailTool) Name() string        { return "tail" }
func (t *TailTool) Description() string { return prompts.ToolDescription("tail") }

func (t *TailTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"filePath": {"type": "string", "description": "The absolute path of the file to read."},
			"lines": {"type": "integer", "minimum": 1, "description": "Return the last N lines (default 50). Mutually exclusive with bytes."},
			"bytes": {"type": "integer", "minimum": 1, "description": "Return the last N bytes instead of lines."},
			"offset": {"type": "integer", "minimum": 0, "description": "With bytes, skip this many trailing bytes before the window (default 0)."}
		},
		"required": ["filePath"]
	}`)
}

func (t *TailTool) IsDangerous(string) bool { return false }

func (t *TailTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		FilePath string `json:"filePath"`
		Lines    int    `json:"lines"`
		Bytes    int64  `json:"bytes"`
		Offset   int64  `json:"offset"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("tail: invalid arguments: %w", err)
	}
	if params.FilePath == "" {
		return "", fmt.Errorf("tail: filePath is required")
	}
	if params.Bytes < 0 || params.Offset < 0 {
		return "", fmt.Errorf("tail: bytes and offset must be non-negative")
	}
	ws := workspaceOf(t.WS, t.PV)
	resolved, err := ws.ResolvePath(params.FilePath)
	if err != nil {
		return "", fmt.Errorf("tail: %w", err)
	}
	info, err := ws.Stat(ctx, resolved)
	if err != nil {
		return "", fmt.Errorf("tail: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("tail: %s is a directory", resolved)
	}
	if info.Size() == 0 {
		return "", nil
	}

	if params.Bytes > 0 {
		return tailBytes(ctx, ws, resolved, info.Size(), params.Offset, params.Bytes)
	}
	lines := params.Lines
	if lines <= 0 {
		lines = tailDefaultLines
	}
	if lines > tailMaxLines {
		lines = tailMaxLines
	}
	return tailLines(ctx, ws, resolved, info.Size(), lines)
}

func tailBytes(ctx context.Context, ws Workspace, path string, size, offset, n int64) (string, error) {
	if offset >= size {
		return "", nil
	}
	end := size - offset
	start := end - n
	if start < 0 {
		start = 0
	}
	data, err := ws.ReadRange(ctx, path, start, end-start)
	if err != nil {
		return "", fmt.Errorf("tail: %w", err)
	}
	// A byte window is an exact slice: do not prepend a marker, or the caller
	// cannot use it to inspect raw bytes. The read range is explicit.
	return keepTail(string(data), toolResultMaxBytes), nil
}

func tailLines(ctx context.Context, ws Workspace, path string, size int64, want int) (string, error) {
	if !ws.Local() {
		// A sandbox's ReadRange materializes the whole file, so chunking
		// backward there would re-read it once per chunk. Read it once and
		// take the tail in memory instead.
		data, err := ws.ReadFile(ctx, path)
		if err != nil {
			return "", fmt.Errorf("tail: %w", err)
		}
		return formatTailLines(string(data), want, false), nil
	}

	var chunks [][]byte
	start := size
	newlines := 0
	for start > 0 && newlines <= want && size-start < tailMaxBackBytes {
		readStart := start - tailChunkBytes
		if readStart < 0 {
			readStart = 0
		}
		chunk, err := ws.ReadRange(ctx, path, readStart, start-readStart)
		if err != nil {
			return "", fmt.Errorf("tail: %w", err)
		}
		chunks = append(chunks, chunk)
		newlines += bytes.Count(chunk, []byte{'\n'})
		start = readStart
	}

	var b strings.Builder
	for i := len(chunks) - 1; i >= 0; i-- {
		b.Write(chunks[i])
	}
	text := b.String()
	omitted := start > 0
	if omitted {
		// The window begins mid-line; drop that partial line.
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		} else {
			text = ""
		}
	}
	return formatTailLines(text, want, omitted), nil
}

func formatTailLines(text string, want int, omitted bool) string {
	lines := strings.Split(text, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if len(lines) > want {
		lines = lines[len(lines)-want:]
		omitted = true
	}
	out := strings.Join(lines, "\n")
	if omitted {
		out = "...[earlier lines omitted]...\n" + out
	}
	return keepTail(out, toolResultMaxBytes)
}
