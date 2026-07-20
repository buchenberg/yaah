package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// JSONQueryTool reads, writes, and deletes values in JSON files using dot-notation paths.
// Reading (no set value) is safe; writing and deleting are dangerous.
type JSONQueryTool struct{}

func (t *JSONQueryTool) Name() string { return "json_query" }
func (t *JSONQueryTool) Description() string {
	return "Read, write, or delete a value in a JSON file using a dot-notation path."
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
	params.File = expandHomeDir(params.File)

	data, err := os.ReadFile(params.File)
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
		return t.doWrite(params.File, data, root, params.Path, params.Set)
	case "delete":
		return t.doDelete(params.File, data, root, params.Path)
	default:
		return "", fmt.Errorf("json_query: unsupported action %q", params.Action)
	}
}

func (t *JSONQueryTool) doRead(root any, path string) (string, error) {
	if path == "" {
		result, _ := json.MarshalIndent(root, "", "  ")
		result = truncateBytes(result, toolResultMaxLen)
		return string(result), nil
	}

	val, err := resolveJSONPath(root, path)
	if err != nil {
		return "", err
	}

	result, _ := json.MarshalIndent(val, "", "  ")
	result = truncateBytes(result, toolResultMaxLen)
	return string(result), nil
}

func (t *JSONQueryTool) doWrite(filePath string, data []byte, root any, path, setValue string) (string, error) {
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

	newData, _ := json.MarshalIndent(root, "", "  ")
	newData = append(newData, '\n')

	if err := os.WriteFile(filePath, newData, 0o644); err != nil {
		return "", fmt.Errorf("json_query: write file: %w", err)
	}

	return fmt.Sprintf("Set %s to %v", path, setValue), nil
}

func (t *JSONQueryTool) doDelete(filePath string, data []byte, root any, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("json_query: path is required for delete")
	}

	if err := deleteJSONPath(root, path); err != nil {
		return "", fmt.Errorf("json_query: %w", err)
	}

	newData, _ := json.MarshalIndent(root, "", "  ")
	newData = append(newData, '\n')

	if err := os.WriteFile(filePath, newData, 0o644); err != nil {
		return "", fmt.Errorf("json_query: write file: %w", err)
	}

	return fmt.Sprintf("Deleted %s", path), nil
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

// setJSONPath sets a value in a parsed JSON structure at the given path.
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
			if err != nil || idx < 0 || idx >= len(v) {
				return fmt.Errorf("json_query: index %d out of bounds at %s", idx, pathPrefix(parts, i))
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

// deleteJSONPath removes a key from a parsed JSON structure at the given path.
func deleteJSONPath(root any, path string) error {
	parts := splitPath(path)
	if len(parts) == 0 {
		return fmt.Errorf("delete: empty path")
	}

	if len(parts) == 1 {
		switch v := root.(type) {
		case map[string]any:
			delete(v, parts[0])
			return nil
		case []any:
			idx, err := strconv.Atoi(parts[0])
			if err != nil {
				return fmt.Errorf("json_query: expected array index, got %q", parts[0])
			}
			if idx < 0 || idx >= len(v) {
				return fmt.Errorf("json_query: index %d out of bounds (length %d)", idx, len(v))
			}
			// replace with zero value — can't shrink array
			v[idx] = nil
			return nil
		default:
			return fmt.Errorf("json_query: cannot delete from %T", root)
		}
	}

	// Navigate to parent.
	current := root
	for i := 0; i < len(parts)-1; i++ {
		part := parts[i]
		switch v := current.(type) {
		case map[string]any:
			child, ok := v[part]
			if !ok {
				return fmt.Errorf("json_query: key %q not found", part)
			}
			current = child
		case []any:
			idx, err := strconv.Atoi(part)
			if err != nil {
				return fmt.Errorf("json_query: expected array index, got %q", part)
			}
			if idx < 0 || idx >= len(v) {
				return fmt.Errorf("json_query: index %d out of bounds", idx)
			}
			current = v[idx]
		default:
			return fmt.Errorf("json_query: cannot traverse into %T", current)
		}
	}

	final := parts[len(parts)-1]
	if m, ok := current.(map[string]any); ok {
		if _, exists := m[final]; !exists {
			return fmt.Errorf("json_query: key %q not found", final)
		}
		delete(m, final)
		return nil
	}
	return fmt.Errorf("json_query: cannot delete from %T at %s", current, final)
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

func truncateBytes(b []byte, max int) []byte {
	if len(b) <= max {
		return b
	}
	return append(b[:max], []byte("\n...[truncated]...")...)
}
