package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// UnknownArgumentError reports a tool call that named a key the tool does not
// accept. The model sees the allowed list so the next attempt can be right,
// instead of the typo silently changing behavior (the reason this exists:
// "backgroud": true ran a foreground dispatch).
type UnknownArgumentError struct {
	Tool      string
	Arguments []string
	Allowed   []string
}

func (e *UnknownArgumentError) Error() string {
	return fmt.Sprintf("%s: unknown argument(s) %s; allowed: %s",
		e.Tool, quoteList(e.Arguments), strings.Join(e.Allowed, ", "))
}

// DecodeStrictObject decodes a JSON object and rejects every key outside
// allowed. Tools that need numeric-string coercion decode the returned map,
// coerce, then unmarshal into their typed params — the strict check happens
// before any behavior is chosen.
func DecodeStrictObject(tool, args string, allowed ...string) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(args), &raw); err != nil {
		return nil, fmt.Errorf("%s: invalid arguments: %w", tool, err)
	}
	permitted := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		permitted[key] = true
	}
	var unknown []string
	for key := range raw {
		if !permitted[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		sorted := append([]string(nil), allowed...)
		sort.Strings(sorted)
		return nil, &UnknownArgumentError{Tool: tool, Arguments: unknown, Allowed: sorted}
	}
	return raw, nil
}

func quoteList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = fmt.Sprintf("%q", value)
	}
	return strings.Join(quoted, ", ")
}
