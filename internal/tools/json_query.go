package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/buchenberg/yaah/internal/prompts"
)

// JSONQueryTool reads, writes, and deletes values in JSON files using dot-notation paths.
// Reading (no set value) is safe; writing and deleting are dangerous.
type JSONQueryTool struct {
	PV *PathValidator
	WS Workspace
}

var (
	_ PathValidatorSetter = (*JSONQueryTool)(nil)
	_ WorkspaceSetter     = (*JSONQueryTool)(nil)
)

func (t *JSONQueryTool) SetPathValidator(pv *PathValidator) { t.PV = pv }
func (t *JSONQueryTool) SetWorkspace(ws Workspace)          { t.WS = ws }

func (t *JSONQueryTool) Name() string { return "json_query" }
func (t *JSONQueryTool) Description() string {
	return prompts.ToolDescription("json_query")
}

func (t *JSONQueryTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"file": {"type": "string", "description": "Path to the JSON file"},
			"path": {"type": "string", "description": "Dot-notation path to the value (e.g. 'dependencies.react', 'store.books[0].title')"},
			"action": {"type": "string", "enum": ["read", "write", "delete"], "description": "Action to perform (default: read if no set value, write if set value provided)"},
			"set": {"type": "string", "description": "New value as a JSON-encoded string (required for write)"}
		},
		"required": ["file"]
	}`)
}

func (t *JSONQueryTool) IsDangerous(argsJSON string) bool {
	var params struct {
		Set    string `json:"set"`
		Action string `json:"action"`
	}
	json.Unmarshal([]byte(argsJSON), &params)
	return params.Action == "write" || params.Action == "delete" || params.Set != ""
}

func (t *JSONQueryTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		File   string `json:"file"`
		Path   string `json:"path"`
		Action string `json:"action"`
		Set    string `json:"set"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("json_query: invalid arguments: %w", err)
	}
	if params.File == "" {
		return "", fmt.Errorf("json_query: file is required")
	}
	if params.Action == "" {
		if params.Set != "" {
			params.Action = "write"
		} else {
			params.Action = "read"
		}
	}
	if params.Path == "" && params.Action != "read" {
		return "", fmt.Errorf("json_query: path is required for %s", params.Action)
	}
	ws := workspaceOf(t.WS, t.PV)
	resolved, err := ws.ResolvePath(params.File)
	if err != nil {
		return "", err
	}
	params.File = resolved

	data, err := ws.ReadFile(ctx, params.File)
	if err != nil {
		return "", fmt.Errorf("json_query: %w", err)
	}

	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return "", fmt.Errorf("json_query: invalid JSON: %w", err)
	}

	switch params.Action {
	case "read":
		return t.doRead(root, params.Path)
	case "write":
		return t.doWrite(ctx, ws, params.File, data, root, params.Path, params.Set)
	case "delete":
		return t.doDelete(ctx, ws, params.File, data, root, params.Path)
	default:
		return "", fmt.Errorf("json_query: unsupported action %q", params.Action)
	}
}

func (t *JSONQueryTool) doRead(root any, path string) (string, error) {
	target := root
	if path != "" {
		val, err := resolveJSONPath(root, path)
		if err != nil {
			return "", err
		}
		target = val
	}

	result, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		return "", fmt.Errorf("json_query: encode value: %w", err)
	}
	return truncateResult(string(result)), nil
}

func (t *JSONQueryTool) doWrite(ctx context.Context, ws Workspace, filePath string, data []byte, root any, path, setValue string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("json_query: path is required for write")
	}

	// Parse the set value as JSON so numbers/objects/booleans work.
	var parsed any
	if err := json.Unmarshal([]byte(setValue), &parsed); err != nil {
		return "", fmt.Errorf("json_query: invalid set value (must be valid JSON): %w", err)
	}

	if err := setJSONPath(root, path, parsed); err != nil {
		return "", fmt.Errorf("json_query: %w", err)
	}

	newData, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", fmt.Errorf("json_query: encode JSON: %w", err)
	}
	newData = append(newData, '\n')

	if err := ws.WriteFile(ctx, filePath, newData, 0o644); err != nil {
		return "", fmt.Errorf("json_query: write file: %w", err)
	}

	var report multiResult
	report.donef("Set %s to %s", path, setValue)
	if string(newData) != string(data) {
		report.donef("note: rewrote the whole file with 2-space indentation and sorted object keys")
	}
	return report.Report(fmt.Sprintf("json_query: wrote %s", filePath))
}

func (t *JSONQueryTool) doDelete(ctx context.Context, ws Workspace, filePath string, data []byte, root any, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("json_query: path is required for delete")
	}

	newRoot, err := deleteJSONPath(root, path)
	if err != nil {
		return "", fmt.Errorf("json_query: %w", err)
	}

	newData, err := json.MarshalIndent(newRoot, "", "  ")
	if err != nil {
		return "", fmt.Errorf("json_query: encode JSON: %w", err)
	}
	newData = append(newData, '\n')

	if err := ws.WriteFile(ctx, filePath, newData, 0o644); err != nil {
		return "", fmt.Errorf("json_query: write file: %w", err)
	}

	var report multiResult
	if string(newData) != string(data) {
		report.donef("note: rewrote the whole file with 2-space indentation and sorted object keys")
	}
	return report.Report(fmt.Sprintf("Deleted %s", path))
}

// resolveJSONPath traverses a parsed JSON structure using a dot-notation path.
// Supports: "key", "nested.key", "array[0]", "array[0].key", "nested.array[1].prop".
func resolveJSONPath(root any, path string) (any, error) {
	current := root
	parts := splitPath(path)

	for i, part := range parts {
		switch v := current.(type) {
		case map[string]any:
			key, idx := splitBracketPart(part)
			child, ok := v[key]
			if !ok {
				return nil, fmt.Errorf("json_query: key %q not found at %s", key, pathPrefix(parts, i))
			}
			if idx >= 0 {
				val, err := resolveIndex(child, idx, pathPrefix(parts, i))
				if err != nil {
					return nil, err
				}
				current = val
			} else {
				current = child
			}
		case []any:
			idx, err := strconv.Atoi(part)
			if idx < 0 {
				return nil, fmt.Errorf("json_query: expected array index, got %q at %s", part, pathPrefix(parts, i))
			}
			if err != nil {
				return nil, fmt.Errorf("json_query: expected array index, got %q at %s", part, pathPrefix(parts, i))
			}
			if idx >= len(v) {
				return nil, fmt.Errorf("json_query: index %d out of bounds (length %d) at %s", idx, len(v), pathPrefix(parts, i))
			}
			current = v[idx]
		default:
			return nil, fmt.Errorf("json_query: cannot traverse into %T at %s", current, pathPrefix(parts, i))
		}
	}

	return current, nil
}

// setJSONPath sets a value in a parsed JSON structure at the given path. The
// path's containers must already exist; this function does not create keys.
func setJSONPath(root any, path string, value any) error {
	parts := splitPath(path)
	if len(parts) == 0 {
		return fmt.Errorf("set: empty path")
	}

	// Navigate to the parent, create containers as needed.
	current := root
	for i := 0; i < len(parts)-1; i++ {
		part := parts[i]
		switch v := current.(type) {
		case map[string]any:
			key, idx := splitBracketPart(part)
			child, ok := v[key]
			if !ok {
				return fmt.Errorf("json_query: key %q not found at %s", key, pathPrefix(parts, i))
			}
			if idx >= 0 {
				val, err := resolveIndex(child, idx, pathPrefix(parts, i))
				if err != nil {
					return err
				}
				current = val
			} else {
				current = child
			}
		case []any:
			idx, err := strconv.Atoi(part)
			if err != nil {
				return fmt.Errorf("json_query: expected numeric array index at %s, got %q", pathPrefix(parts, i), part)
			}
			if idx < 0 || idx >= len(v) {
				return fmt.Errorf("json_query: index %d out of bounds (array length %d) at %s", idx, len(v), pathPrefix(parts, i))
			}
			current = v[idx]
		default:
			return fmt.Errorf("json_query: cannot traverse into %T at %s", current, pathPrefix(parts, i))
		}
	}

	// Set the final value.
	final := parts[len(parts)-1]
	switch v := current.(type) {
	case map[string]any:
		key, idx := splitBracketPart(final)
		if idx >= 0 {
			arr, ok := v[key].([]any)
			if !ok {
				return fmt.Errorf("json_query: %s is not an array", key)
			}
			if idx < 0 || idx >= len(arr) {
				return fmt.Errorf("json_query: index %d out of bounds (length %d)", idx, len(arr))
			}
			arr[idx] = value
		} else {
			v[key] = value
		}
	case []any:
		idx, err := strconv.Atoi(final)
		if err != nil || idx < 0 || idx >= len(v) {
			return fmt.Errorf("json_query: index %d out of bounds (length %d)", idx, len(v))
		}
		v[idx] = value
	default:
		return fmt.Errorf("json_query: cannot set on %T", current)
	}

	return nil
}

// deleteJSONPath removes the value at path and returns the (possibly new)
// root. Deleting an element of the root array changes the slice header, so the
// caller must marshal the returned value rather than the original root.
func deleteJSONPath(root any, path string) (any, error) {
	parts := splitPath(path)
	if len(parts) == 0 {
		return nil, fmt.Errorf("delete: empty path")
	}
	return deleteJSONPathParts(root, parts)
}

func deleteJSONPathParts(node any, parts []string) (any, error) {
	part := parts[0]

	switch v := node.(type) {
	case map[string]any:
		key, idx := splitBracketPart(part)
		if idx >= 0 {
			arr, ok := v[key].([]any)
			if !ok {
				return nil, fmt.Errorf("json_query: %s is not an array", key)
			}
			if idx < 0 || idx >= len(arr) {
				return nil, fmt.Errorf("json_query: index %d out of bounds (length %d)", idx, len(arr))
			}
			if len(parts) == 1 {
				v[key] = append(arr[:idx], arr[idx+1:]...)
				return v, nil
			}
			child, err := deleteJSONPathParts(arr[idx], parts[1:])
			if err != nil {
				return nil, err
			}
			arr[idx] = child
			return v, nil
		}

		child, ok := v[key]
		if !ok {
			return nil, fmt.Errorf("json_query: key %q not found", key)
		}
		if len(parts) == 1 {
			delete(v, key)
			return v, nil
		}
		newChild, err := deleteJSONPathParts(child, parts[1:])
		if err != nil {
			return nil, err
		}
		v[key] = newChild
		return v, nil

	case []any:
		idx, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("json_query: expected array index, got %q", part)
		}
		if idx < 0 || idx >= len(v) {
			return nil, fmt.Errorf("json_query: index %d out of bounds (length %d)", idx, len(v))
		}
		if len(parts) == 1 {
			return append(v[:idx], v[idx+1:]...), nil
		}
		newChild, err := deleteJSONPathParts(v[idx], parts[1:])
		if err != nil {
			return nil, err
		}
		v[idx] = newChild
		return v, nil

	default:
		return nil, fmt.Errorf("json_query: cannot delete from %T", node)
	}
}

func splitPath(path string) []string {
	path = strings.TrimPrefix(path, "$")
	path = strings.TrimPrefix(path, ".")
	var parts []string
	for _, segment := range strings.Split(path, ".") {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		// A segment like "items[1]" stays as one part.
		parts = append(parts, segment)
	}
	if len(parts) == 0 && path != "" {
		parts = append(parts, path)
	}
	return parts
}

func splitBracketPart(part string) (string, int) {
	idx := strings.Index(part, "[")
	if idx < 0 {
		return part, -1
	}
	key := part[:idx]
	n, err := strconv.Atoi(part[idx+1 : len(part)-1])
	if err != nil {
		return part, -1
	}
	return key, n
}

func resolveIndex(val any, idx int, context string) (any, error) {
	arr, ok := val.([]any)
	if !ok {
		return nil, fmt.Errorf("json_query: expected array at %s, got %T", context, val)
	}
	if idx < 0 || idx >= len(arr) {
		return nil, fmt.Errorf("json_query: index %d out of bounds (length %d) at %s", idx, len(arr), context)
	}
	return arr[idx], nil
}

func pathPrefix(parts []string, upTo int) string {
	var sb strings.Builder
	for i := 0; i <= upTo && i < len(parts); i++ {
		if i > 0 && !strings.HasPrefix(parts[i], "[") {
			sb.WriteByte('.')
		}
		sb.WriteString(parts[i])
	}
	return sb.String()
}
