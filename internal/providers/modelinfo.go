package providers

import (
	"strings"
)

var modelWindows = map[string]int{
	"deepseek-v4-pro":   1048576,
	"deepseek-v4-flash": 1048576,
	"deepseek-flash":    1048576,
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

// modelOutputLimits maps known models to a conservative maximum output-token
// cap. It is used only to keep output-budget recovery from raising max_tokens
// past a provider limit; it is not a claim about the model's full capacity.
// Unknown models return 0 and rely on the retry ladder's global bound.
var modelOutputLimits = map[string]int{
	"gpt-4o":      16384,
	"gpt-4o-mini": 16384,
	"gpt-4-turbo": 4096,
	"gpt-4":       8192,

	"gpt-3.5-turbo": 4096,

	"deepseek-v4-pro":   32768,
	"deepseek-v4-flash": 32768,
	"deepseek-flash":    32768,
	"deepseek-v3":       8192,

	"glm-5.3":       32768,
	"glm-5.3-flash": 32768,

	"claude-3.5-sonnet": 8192,
	"claude-3.5-haiku":  8192,
	"claude-3-opus":     4096,
	"claude-3-sonnet":   4096,
	"claude-3-haiku":    4096,
}

// ResolveOutputLimit returns the known output-token ceiling for modelName,
// or 0 when the model is not in the conservative table.
func ResolveOutputLimit(modelName string) int {
	name := strings.ToLower(sanitizeModelName(modelName))
	return modelOutputLimits[name]
}

// thinkingModels are models that use interleaved reasoning and require
// reasoning_content to be passed back on every assistant message. Keyed by
// sanitized model name (after the last "/"). Prefix matching is used for
// model families (e.g. "deepseek-r1" matches "deepseek-r1-0528").
var thinkingModels = map[string]bool{
	"deepseek-r1":         true,
	"deepseek-r1-0528":    true,
	"deepseek-r1-distill": true,
	"deepseek-flash":      true,
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
