package llm

import (
	"fmt"
	"regexp"
	"strings"
)

var overflowPatterns = []*regexp.Regexp{
	// opencode / Kilo Code patterns (15+ providers)
	regexp.MustCompile(`(?i)context.length`),
	regexp.MustCompile(`(?i)context length`),
	regexp.MustCompile(`(?i)context_length`),
	regexp.MustCompile(`(?i)context window`),
	regexp.MustCompile(`(?i)context.window`),
	regexp.MustCompile(`(?i)too long`),
	regexp.MustCompile(`(?i)too many tokens`),
	regexp.MustCompile(`(?i)maximum context`),
	regexp.MustCompile(`(?i)max context`),
	regexp.MustCompile(`(?i)max_context`),
	regexp.MustCompile(`(?i)token limit`),
	regexp.MustCompile(`(?i)token_limit`),
	regexp.MustCompile(`(?i)reduce the length`),
	regexp.MustCompile(`(?i)please reduce`),
	regexp.MustCompile(`(?i)request too large`),
	regexp.MustCompile(`(?i)too large`),

	// Provider-specific patterns
	regexp.MustCompile(`(?i)messages with role .tool. must be a response`),
	regexp.MustCompile(`(?i)reasoning_content.*must be passed back`),

	// HTTP status patterns in error text
	regexp.MustCompile(`(?i)\b413\b`),
	regexp.MustCompile(`(?i)payload too large`),
	regexp.MustCompile(`(?i)request entity too large`),

	// Anthropic
	regexp.MustCompile(`(?i)prompt is too long`),

	// Google
	regexp.MustCompile(`(?i)request payload size exceeds`),

	// Groq / local
	regexp.MustCompile(`(?i)input length.*exceeds`),
	regexp.MustCompile(`(?i)exceeds the maximum`),
}

var payloadTooLargePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b413\b`),
	regexp.MustCompile(`(?i)payload too large`),
	regexp.MustCompile(`(?i)request too large`),
	regexp.MustCompile(`(?i)entity too large`),
	regexp.MustCompile(`(?i)request payload size exceeds`),
}

func IsOverflowError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, p := range overflowPatterns {
		if p.MatchString(msg) {
			return true
		}
	}
	return false
}

func IsPayloadTooLarge(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, p := range payloadTooLargePatterns {
		if p.MatchString(msg) {
			return true
		}
	}
	return false
}

func WrapOverflowError(err error) error {
	if err == nil {
		return nil
	}
	if IsOverflowError(err) {
		return fmt.Errorf("context overflow: %w", err)
	}
	return err
}

func SummarizeError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	const maxLen = 200
	if len(msg) > maxLen {
		return strings.TrimSpace(msg[:maxLen]) + "..."
	}
	return msg
}
