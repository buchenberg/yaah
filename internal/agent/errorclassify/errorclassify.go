// Package errorclassify provides structured error classification for
// LLM provider failures. It replaces ad-hoc string matching with a
// taxonomy mirroring hermes-agent's error_classifier.py.
package errorclassify

import "fmt"

// ErrorReason identifies the category of a provider failure.
type ErrorReason int

const (
	ReasonUnknown            ErrorReason = iota // unclassifiable — retry with backoff
	ReasonAuth                                  // 401/403 — refresh/rotate credentials
	ReasonAuthPermanent                         // auth failed after rotation — abort
	ReasonBilling                               // 402/credit exhaustion — rotate immediately
	ReasonRateLimit                             // 429/quota — backoff then rotate
	ReasonOverloaded                            // 503/529 — provider overloaded, backoff
	ReasonServerError                           // 500/502 — internal server error, retry
	ReasonTimeout                               // connection/read timeout — retry
	ReasonContextOverflow                       // context window exceeded — compress
	ReasonPayloadTooLarge                       // 413 — compress payload
	ReasonModelNotFound                         // 404/invalid model — fallback to different model
	ReasonContentPolicyBlocked                  // safety filter — do NOT retry unchanged
	ReasonProviderPolicyBlocked                 // aggregator data/privacy block
	ReasonFormatError                           // 400 bad request — abort or strip+retry
)

func (r ErrorReason) String() string {
	switch r {
	case ReasonUnknown:
		return "unknown"
	case ReasonAuth:
		return "auth"
	case ReasonAuthPermanent:
		return "auth_permanent"
	case ReasonBilling:
		return "billing"
	case ReasonRateLimit:
		return "rate_limit"
	case ReasonOverloaded:
		return "overloaded"
	case ReasonServerError:
		return "server_error"
	case ReasonTimeout:
		return "timeout"
	case ReasonContextOverflow:
		return "context_overflow"
	case ReasonPayloadTooLarge:
		return "payload_too_large"
	case ReasonModelNotFound:
		return "model_not_found"
	case ReasonContentPolicyBlocked:
		return "content_policy_blocked"
	case ReasonProviderPolicyBlocked:
		return "provider_policy_blocked"
	case ReasonFormatError:
		return "format_error"
	default:
		return fmt.Sprintf("ErrorReason(%d)", r)
	}
}

// ErrorMeta is the context available to the classifier at the call site.
type ErrorMeta struct {
	StatusCode  int    // HTTP status code (0 if unknown)
	Provider    string // e.g. "openrouter", "anthropic"
	Model       string // e.g. "claude-sonnet-4"
	NumMessages int    // number of messages in the request
}

// ClassifiedError is the result of error classification with concrete
// recovery hints. The retry loop acts on these hints — it never
// re-classifies the raw error.
type ClassifiedError struct {
	Reason ErrorReason
	Meta   ErrorMeta

	// Recovery hints
	Retryable        bool // can safely retry after backoff
	ShouldCompress   bool // compress context before retrying
	ShouldRotateCred bool // switch to a different credential / fallback provider
	ShouldAbort      bool // surface error to user immediately (no fallback)

	Message string // human-readable classification summary (for logs)
}

func (c ClassifiedError) Error() string { return fmt.Sprintf("%s: %s", c.Reason, c.Message) }
