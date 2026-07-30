package providers

import (
	"strings"
)

var modelWindows = map[string]int{
	"deepseek-v4-pro":   128000,
	"deepseek-v4-flash": 128000,
	"deepseek-v3":       65536,

	"gpt-4o":        128000,
	"gpt-4o-mini":   128000,
	"gpt-4-turbo":   128000,
	"gpt-4":         8192,
	"gpt-3.5-turbo": 16385,

	"o1":         200000,
	"o1-mini":    128000,
	"o1-preview": 128000,

	"claude-3.5-sonnet": 200000,
	"claude-3.5-haiku":  200000,
	"claude-3-opus":     200000,
	"claude-3-sonnet":   200000,
	"claude-3-haiku":    200000,

	"gemini-2.5-pro":   1048576,
	"gemini-2.5-flash": 1048576,
	"gemini-2.0-flash": 1048576,
	"gemini-1.5-pro":   2097152,
	"gemini-1.5-flash": 1048576,

	"llama-3.1-405b": 131072,
	"llama-3.1-70b":  131072,
	"llama-3.1-8b":   131072,
	"llama-3.2-90b":  131072,
	"llama-3.2-11b":  131072,
	"llama-3.2-3b":   131072,
	"llama-3.3-70b":  131072,
}

// thinkingModels are models that use interleaved reasoning and require
// reasoning_content to be passed back on every assistant message. Keyed by
// sanitized model name (after the last "/"). Prefix matching is used for
// model families (e.g. "deepseek-r1" matches "deepseek-r1-0528").
var thinkingModels = map[string]bool{
	"deepseek-r1":         true,
	"deepseek-r1-0528":    true,
	"deepseek-r1-distill": true,
	"o1":                  true,
	"o1-mini":             true,
	"o1-preview":          true,
	"o3":                  true,
	"o3-mini":             true,
	"o4-mini":             true,
	"qwq-32b":             true,
	"qwq-plus":            true,
	"kimi-k1.5":           true,
	"kimi-k2-thinking":    true,
}

// thinkingPrefixes matches model families where any variant is a thinking
// model (e.g. "o3-" matches "o3-mini", "o3-pro", etc.).
var thinkingPrefixes = []string{
	"o1-", "o3-", "o4-",
	"deepseek-r1",
	"deepseek-v4",
	"qwq-",
}

func sanitizeModelName(name string) string {
	idx := strings.LastIndexByte(name, '/')
	if idx >= 0 {
		return name[idx+1:]
	}
	return name
}

// IsThinkingModel reports whether a model uses interleaved reasoning mode
// and requires reasoning_content on all assistant messages.
func IsThinkingModel(modelName string) bool {
	name := sanitizeModelName(modelName)
	name = strings.ToLower(name)
	if thinkingModels[name] {
		return true
	}
	for _, prefix := range thinkingPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func ResolveWindow(modelName string, configCap int) int {
	discovered, ok := modelWindows[modelName]
	if !ok {
		discovered, ok = modelWindows[sanitizeModelName(modelName)]
	}
	if !ok || discovered <= 0 {
		return configCap
	}
	if configCap > 0 && configCap < discovered {
		return configCap
	}
	return discovered
}
