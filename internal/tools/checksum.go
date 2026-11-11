package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
)

const (
	checksumChunkBytes = 512 * 1024
	checksumMaxPaths   = 1000
)

// ChecksumTool hashes one or more files with SHA-256, optionally comparing a
// pair. It lets a caller verify a transfer or check duplication without
// reading file content into the model.
type ChecksumTool struct {
	PV *PathValidator
	WS Workspace
}

var (
	_ PathValidatorSetter = (*ChecksumTool)(nil)
	_ WorkspaceSetter     = (*ChecksumTool)(nil)
)

func (t *ChecksumTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *ChecksumTool) SetWorkspace(ws Workspace)          { t.WS = ws }

func (t *ChecksumTool) Name() string        { return "checksum" }
func (t *ChecksumTool) Description() string { return prompts.ToolDescription("checksum") }

func (t *ChecksumTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"paths": {
				"type": "array",
				"items": {"type": "string"},
				"minItems": 1,
				"description": "Absolute paths of the files to hash."
			},
			"compare": {
				"type": "boolean",
				"description": "When true, paths must contain exactly two files and the result reports whether their SHA-256 digests match.",
				"default": false
			}
		},
		"required": ["paths"]
	}`)
}

func (t *ChecksumTool) IsDangerous(string) bool { return false }

type fileChecksum struct {
	path string
	hash string
}

func (t *ChecksumTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Paths   []string `json:"paths"`
		Compare bool     `json:"compare"`
	}
	if err := StrictToolArgs("checksum", t.Schema(), args, &params); err != nil {
		return "", err
	}
	if len(params.Paths) == 0 {
		return "", fmt.Errorf("checksum: paths is required")
	}
	if len(params.Paths) > checksumMaxPaths {
		return "", fmt.Errorf("checksum: at most %d paths per call (got %d)", checksumMaxPaths, len(params.Paths))
	}
	if params.Compare && len(params.Paths) != 2 {
		return "", fmt.Errorf("checksum: compare requires exactly two paths (got %d)", len(params.Paths))
	}
	ws := workspaceOf(t.WS, t.PV)
	results := make([]fileChecksum, 0, len(params.Paths))
	for _, raw := range params.Paths {
		resolved, err := ws.ResolvePath(raw)
		if err != nil {
			return "", fmt.Errorf("checksum: %w", err)
		}
		info, err := ws.Stat(ctx, resolved)
		if err != nil {
			return "", fmt.Errorf("checksum: %s: %w", resolved, err)
		}
		if info.IsDir() {
			return "", fmt.Errorf("checksum: %s is a directory", resolved)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("checksum: %s is not a regular file", resolved)
		}
		hash, err := hashWorkspaceFile(ctx, ws, resolved, info.Size())
		if err != nil {
			return "", fmt.Errorf("checksum: %s: %w", resolved, err)
		}
		results = append(results, fileChecksum{path: resolved, hash: hash})
	}

	var b strings.Builder
	for _, r := range results {
		fmt.Fprintf(&b, "%s  %s\n", r.hash, r.path)
	}
	if params.Compare {
		fmt.Fprintf(&b, "equal: %t\n", results[0].hash == results[1].hash)
	}
	return truncateResult(strings.TrimRight(b.String(), "\n")), nil
}

// hashWorkspaceFile streams a local file in chunks. A sandbox workspace
// materializes the file once instead: its ReadRange falls back to a whole-file
// read, so chunking there would re-read the file for every chunk.
func hashWorkspaceFile(ctx context.Context, ws Workspace, path string, size int64) (string, error) {
	h := sha256.New()
	if !ws.Local() {
		data, err := ws.ReadFile(ctx, path)
		if err != nil {
			return "", err
		}
		h.Write(data)
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	for off := int64(0); off < size; off += checksumChunkBytes {
		n := int64(checksumChunkBytes)
		if remaining := size - off; remaining < n {
			n = remaining
		}
		data, err := ws.ReadRange(ctx, path, off, n)
		if err != nil {
			return "", err
		}
		if len(data) == 0 {
			break
		}
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
