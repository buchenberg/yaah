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

	"o1":        200000,
	"o1-mini":   128000,
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

func sanitizeModelName(name string) string {
	idx := strings.LastIndexByte(name, '/')
	if idx >= 0 {
		return name[idx+1:]
	}
	return name
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
