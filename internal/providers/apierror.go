// apierror.go defines the structured error type for non-2xx provider
// responses. errorclassify consumes StatusCode and Code directly via
// errors.As instead of parsing Error() strings, so rewording the
// message can never silently break retry/fallback classification.
package providers

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// APIError is a structured non-2xx response from a provider API.
type APIError struct {
	// StatusCode is the HTTP status of the failed response.
	StatusCode int

	// Code is the provider's machine-readable error code extracted
	// from the JSON body (e.g. "invalid_request_error"), when present.
	Code string

	// Message is the human-readable provider message, when extractable;
	// otherwise the truncated raw body.
	Message string

	// Detail carries request-context appended after the body (message
	// counts, roles, model) so diagnostics stay in one place.
	Detail string
}

// Error renders the same "provider returned NNN: ..." format the code
// base has always produced, so logs and any remaining string matching
// keep working.
func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = "(no body)"
	}
	if e.Detail != "" {
		return fmt.Sprintf("provider returned %d: %s  [%s]", e.StatusCode, msg, e.Detail)
	}
	return fmt.Sprintf("provider returned %d: %s", e.StatusCode, msg)
}

// HTTPStatus returns the HTTP status code (errorclassify's statusCoder
// interface).
func (e *APIError) HTTPStatus() int { return e.StatusCode }

// ProviderCode returns the machine-readable provider error code
// (errorclassify's statusCoder interface).
func (e *APIError) ProviderCode() string { return e.Code }

// newAPIError builds an *APIError from a non-2xx response body. It
// extracts the standard {"error": {"code"/"type", "message"}} shape
// used by OpenAI-compatible and Anthropic APIs and falls back to the
// truncated raw body.
func newAPIError(statusCode int, body []byte) *APIError {
	e := &APIError{StatusCode: statusCode, Message: truncateRunes(strings.TrimSpace(string(body)), 512)}

	var parsed struct {
		Error struct {
			Code    string `json:"code"`
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		if parsed.Error.Message != "" {
			e.Message = parsed.Error.Message
		}
		e.Code = parsed.Error.Code
		if e.Code == "" {
			e.Code = parsed.Error.Type
		}
	}
	return e
}

// truncateRunes caps s at max bytes without splitting a UTF-8 rune.
func truncateRunes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// withRequestDetail appends request context ("msgs=... roles=... model=...")
// to the error's detail field.
func (e *APIError) withRequestDetail(numMessages int, roles string, model string) *APIError {
	e.Detail = fmt.Sprintf("msgs=%d roles=%s model=%s", numMessages, roles, model)
	return e
}
