package errorclassify

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// statusCoder is implemented by structured provider errors (notably
// providers.APIError). Classification reads these typed fields instead
// of parsing Error() strings, so reworded provider messages can never
// silently break retry/fallback classification. Defined consumer-side
// to keep this package dependency-free.
type statusCoder interface {
	HTTPStatus() int
	ProviderCode() string
}

func statusCodeFrom(err error) int {
	var sc statusCoder
	if errors.As(err, &sc) {
		return sc.HTTPStatus()
	}
	return 0
}

func providerCode(err error) string {
	var sc statusCoder
	if errors.As(err, &sc) {
		return sc.ProviderCode()
	}
	return ""
}

// Classify maps a provider error to a ClassifiedError with recovery hints.
// Priority order: typed status/code → status code → error-code string → message patterns → transport heuristics → unknown.
func Classify(err error, meta ErrorMeta) ClassifiedError {
	if err == nil {
		return ClassifiedError{
			Reason:    ReasonUnknown,
			Meta:      meta,
			Retryable: true,
			Message:   "nil error",
		}
	}

	// Typed provider errors carry the structured status — prefer it over
	// whatever the caller extracted from strings.
	if sc := statusCodeFrom(err); sc != 0 {
		meta.StatusCode = sc
	}

	msg := strings.ToLower(err.Error())
	// Structured codes previously only appeared inside raw response bodies
	// embedded in the error string; keep them searchable now that the
	// rendered message carries the parsed text instead.
	if code := providerCode(err); code != "" {
		msg += " " + strings.ToLower(code)
	}

	c := ClassifiedError{
		Meta:      meta,
		Retryable: true, // default: unknown errors are retryable
		Message:   msg,
	}

	// ── 0. Semantic checks that outrank the status code ───────────
	// A provider in thinking mode demanding reasoning_content be replayed is
	// identifiable from the message alone, and its remedy is config, not
	// retry. This MUST precede the status switch: the generic 4xx default
	// reports an opaque `format_error` and aborts, which is exactly the
	// confusion this check removes.
	if matchAny(msg, thinkingRequiredPatterns) {
		c.Reason = ReasonThinkingModeRequired
		c.Retryable = false
		c.ShouldAbort = true
		c.Message = fmt.Sprintf("model %q requires thinking mode but reasoning_content was omitted — set `thinking: true` for this model in config", meta.Model)
		return c
	}
	if matchAny(msg, reasoningContentPatterns) {
		c.Reason = ReasonReasoningContentMissing
		c.ShouldStripReasoning = true
		c.Message = "reasoning_content rejected — strip and retry"
		return c
	}

	// ── 1. Status-code-driven classification ──────────────────────
	switch meta.StatusCode {
	case http.StatusUnauthorized: // 401
		c.Reason = ReasonAuth
		c.ShouldRotateCred = true
		c.Message = "authentication failed — rotate credentials"
		return c
	case http.StatusForbidden: // 403
		// OpenRouter 403 "key limit exceeded" or "spending limit" is billing.
		// Other providers also use 403 for account-plan/credit exhaustion.
		if strings.Contains(msg, "key limit exceeded") ||
			strings.Contains(msg, "spending limit") ||
			matchAny(msg, billingPatterns) {
			c.Reason = ReasonBilling
			c.ShouldRotateCred = true
			c.Message = "billing exhausted (403 variant) — rotate"
			return c
		}
		c.Reason = ReasonAuth
		c.ShouldRotateCred = true
		c.Message = "access forbidden — rotate credentials"
		return c
	case http.StatusPaymentRequired: // 402
		if hasUsageLimit(msg) && hasTransientSignal(msg) {
			c.Reason = ReasonRateLimit
			c.ShouldRotateCred = true
			c.Message = "transient usage limit — backoff then rotate"
			return c
		}
		c.Reason = ReasonBilling
		c.ShouldRotateCred = true
		c.Message = "billing exhausted — rotate provider"
		return c
	case http.StatusTooManyRequests: // 429
		c.Reason = ReasonRateLimit
		c.ShouldRotateCred = true
		c.Message = "rate limited — backoff then rotate"
		return c
	case http.StatusRequestEntityTooLarge: // 413
		c.Reason = ReasonPayloadTooLarge
		c.ShouldCompress = true
		c.Message = "payload too large — compress and retry"
		return c
	case http.StatusNotFound: // 404
		if matchAny(msg, billingPatterns) {
			c.Reason = ReasonBilling
			c.ShouldRotateCred = true
			c.Message = "billing exhausted (404 variant) — rotate"
			return c
		}
		if matchAny(msg, providerPolicyPatterns) {
			c.Reason = ReasonProviderPolicyBlocked
			c.Retryable = false
			c.ShouldAbort = true
			c.Message = "provider policy blocked — abort"
			return c
		}
		if matchAny(msg, modelNotFoundPatterns) {
			c.Reason = ReasonModelNotFound
			c.ShouldRotateCred = true
			c.Message = "model not found — fallback to different model"
			return c
		}
		// Generic 404 — unknown, retryable
		c.Reason = ReasonUnknown
		c.Message = "generic 404 — retry"
		return c
	case http.StatusBadRequest: // 400
		// Overflow and content-policy run before the response_format
		// check: those patterns are specific, while responseFormatPatterns
		// is a broad substring match that a gateway echoing the request
		// can trip for an unrelated 400. A genuine response_format
		// rejection matches neither of the earlier sets, so the ordering
		// only decides the ambiguous messages — and for those,
		// compress-and-retry or a loud abort are the right recoveries,
		// not a pointless format drop.
		if matchAny(msg, contextOverflowPatterns) {
			c.Reason = ReasonContextOverflow
			c.ShouldCompress = true
			c.Message = "context overflow — compress and retry"
			return c
		}
		if matchAny(msg, contentPolicyPatterns) {
			c.Reason = ReasonContentPolicyBlocked
			c.Retryable = false
			c.ShouldAbort = true
			c.Message = "content policy blocked — do not retry unchanged"
			return c
		}
		if matchAny(msg, responseFormatPatterns) {
			c.Reason = ReasonFormatError
			c.Retryable = false
			c.ShouldDropResponseFormat = true
			c.Message = "response_format unsupported — drop and retry"
			return c
		}
		c.Reason = ReasonFormatError
		c.Retryable = false
		c.ShouldAbort = true
		c.Message = "bad request format — abort"
		return c
	case 502, 500:
		// Detect request-validation errors returned as 5xx by some gateways
		if matchAny(msg, requestValidationPatterns) {
			c.Reason = ReasonFormatError
			c.Retryable = false
			c.ShouldAbort = true
			c.Message = "request validation error (5xx) — fast fail"
			return c
		}
		// Ollama and llama.cpp return 500 for context overflow
		if matchAny(msg, contextOverflowPatterns) {
			c.Reason = ReasonContextOverflow
			c.ShouldCompress = true
			c.Message = "context overflow (5xx variant) — compress and retry"
			return c
		}
		c.Reason = ReasonServerError
		c.Message = "server error — retry"
		return c
	case 503, 529:
		c.Reason = ReasonOverloaded
		c.Message = "provider overloaded — backoff"
		return c
	default:
		// Other 4xx — non-retryable
		if meta.StatusCode >= 400 && meta.StatusCode < 500 {
			c.Reason = ReasonFormatError
			c.Retryable = false
			c.ShouldAbort = true
			c.Message = "client error — abort"
			return c
		}
		// Other 5xx — retryable
		if meta.StatusCode >= 500 && meta.StatusCode < 600 {
			c.Reason = ReasonServerError
			c.Message = "server error — retry"
			return c
		}
	}

	// ── 2. Status-code-agnostic pattern matching ──────────────────

	// Output-budget exhaustion: the model spent the entire max_tokens
	// budget (reasoning models bill thinking tokens against it) before
	// emitting any content. This is not context overflow — compaction
	// cannot reclaim output budget, so retrying unchanged only repeats
	// the failure.
	if strings.Contains(msg, "produced no content") &&
		(strings.Contains(msg, "finish_reason=length") || strings.Contains(msg, "output token budget")) {
		c.Reason = ReasonOutputBudget
		c.Retryable = false
		c.Message = "output token budget exhausted — raise max_tokens for this call (provider default or caller cap; reasoning may consume the whole budget)"
		return c
	}

	// Empty streamed/non-streamed response with known finish_reason — the
	// model may be hitting a context limit without returning a clear error
	// code (DeepSeek v4-pro is known to do this).
	if strings.Contains(msg, "produced no content") {
		c.Reason = ReasonContextOverflow
		c.ShouldCompress = true
		c.Message = "empty response — likely context overflow, compress and retry"
		return c
	}

	if matchAny(msg, contextOverflowPatterns) {
		c.Reason = ReasonContextOverflow
		c.ShouldCompress = true
		c.Message = "context overflow — compress and retry"
		return c
	}
	if matchAny(msg, billingPatterns) {
		c.Reason = ReasonBilling
		c.ShouldRotateCred = true
		c.Message = "billing/credit exhausted — rotate"
		return c
	}
	if matchAny(msg, rateLimitPatterns) {
		c.Reason = ReasonRateLimit
		c.ShouldRotateCred = true
		c.Message = "rate limited — backoff then rotate"
		return c
	}
	if matchAny(msg, authPatterns) {
		c.Reason = ReasonAuth
		c.ShouldRotateCred = true
		c.Message = "auth failure — rotate credentials"
		return c
	}
	if matchAny(msg, modelNotFoundPatterns) {
		c.Reason = ReasonModelNotFound
		c.ShouldRotateCred = true
		c.Message = "model not found — fallback"
		return c
	}
	if matchAny(msg, contentPolicyPatterns) {
		c.Reason = ReasonContentPolicyBlocked
		c.Retryable = false
		c.ShouldAbort = true
		c.Message = "content policy blocked — abort"
		return c
	}
	if matchAny(msg, providerPolicyPatterns) {
		c.Reason = ReasonProviderPolicyBlocked
		c.Retryable = false
		c.ShouldAbort = true
		c.Message = "provider policy blocked — abort"
		return c
	}
	if matchAny(msg, timeoutPatterns) {
		c.Reason = ReasonTimeout
		c.Message = "request timed out — retry"
		return c
	}
	if matchAny(msg, serverDisconnectPatterns) {
		// Large session + disconnect → possible context overflow
		if meta.NumMessages > 20 {
			c.Reason = ReasonContextOverflow
			c.ShouldCompress = true
			c.Message = "server disconnect with large session — compress"
			return c
		}
		c.Reason = ReasonTimeout
		c.Message = "server disconnected — retry"
		return c
	}
	if matchAny(msg, sslTransientPatterns) {
		c.Reason = ReasonTimeout
		c.Message = "SSL transient failure — retry"
		return c
	}
	if matchAny(msg, payloadTooLargePatterns) {
		c.Reason = ReasonPayloadTooLarge
		c.ShouldCompress = true
		c.Message = "payload too large (message pattern) — compress"
		return c
	}

	// ── 3. Transport error type names ─────────────────────────────
	lowerMsg := strings.ToLower(msg)
	for _, t := range transportErrorTypes {
		if strings.Contains(lowerMsg, strings.ToLower(t)) {
			c.Reason = ReasonTimeout
			c.Message = "transport error — retry"
			return c
		}
	}

	// ── 4. Fallback ───────────────────────────────────────────────
	c.Reason = ReasonUnknown
	c.Message = "unclassified error — retry with backoff"
	return c
}

// IsContextOverflow is a convenience wrapper that classifies an error and
// returns true if the reason is ReasonContextOverflow.
func IsContextOverflow(err error, meta ErrorMeta) bool {
	if err == nil {
		return false
	}
	return Classify(err, meta).Reason == ReasonContextOverflow
}

func matchAny(msg string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// hasUsageLimit returns true if message contains a usage-limit pattern.
func hasUsageLimit(msg string) bool {
	return matchAny(msg, usageLimitPatterns)
}

// hasTransientSignal returns true if message contains a transient signal.
func hasTransientSignal(msg string) bool {
	return matchAny(msg, usageLimitTransientSignals)
}

// ── Pattern catalogs (derived from hermes-agent error_classifier.py) ─────

var contextOverflowPatterns = []string{
	"context length",
	"context size",
	"maximum context",
	"token limit",
	"tokens limit",
	"too many tokens",
	"reduce the length",
	"exceeds the limit",
	"context window",
	"prompt is too long",
	"prompt exceeds max length",
	"max tokens",
	"max_tokens",
	"maximum number of tokens",
	// vLLM / local inference server patterns
	"exceeds the max_model_len",
	"max_model_len",
	"prompt length",
	"input is too long",
	"maximum model length",
	// Ollama patterns
	"context length exceeded",
	"truncating input",
	// llama.cpp / llama-server patterns
	"slot context",
	"n_ctx_slot",
	// Chinese error messages
	"超过最大长度",
	"上下文长度",
	// AWS Bedrock Converse API
	"max input token",
	"input token",
	"exceeds the maximum number of input tokens",
	"requested token count",
}

var billingPatterns = []string{
	"insufficient credits",
	"insufficient_quota",
	"insufficient balance",
	"credit balance",
	"credits exhausted",
	"credits have been exhausted",
	"no usable credits",
	"top up your credits",
	"payment required",
	"billing hard limit",
	"exceeded your current quota",
	"account is deactivated",
	"plan does not include",
	"out of funds",
	"run out of funds",
	"balance_depleted",
	"model_not_supported_on_free_tier",
	"not available on the free tier",
	"key limit exceeded",
	"spending limit",
}

var rateLimitPatterns = []string{
	"rate limit",
	"rate_limit",
	"too many requests",
	"throttled",
	"requests per minute",
	"tokens per minute",
	"requests per day",
	"try again in",
	"please retry after",
	"resource_exhausted",
	"rate increased too quickly",
	"throttlingexception",
	"too many concurrent requests",
	"servicequotaexceededexception",
}

var usageLimitPatterns = []string{
	"usage limit",
	"quota",
	"limit exceeded",
	"key limit exceeded",
}

var usageLimitTransientSignals = []string{
	"try again",
	"retry",
	"resets at",
	"reset in",
	"wait",
	"requests remaining",
	"periodic",
	"window",
}

var payloadTooLargePatterns = []string{
	"request entity too large",
	"payload too large",
	"error code: 413",
}

var authPatterns = []string{
	"invalid api key",
	"invalid_api_key",
	"authentication",
	"unauthorized",
	"forbidden",
	"invalid token",
	"token expired",
	"token revoked",
	"access denied",
}

var modelNotFoundPatterns = []string{
	"is not a valid model",
	"invalid model",
	"model not found",
	"model_not_found",
	"does not exist",
	"no such model",
	"unknown model",
	"unsupported model",
}

var contentPolicyPatterns = []string{
	"flagged for possible cybersecurity risk",
	"trusted access for cyber",
	"violates our usage policies",
	"violates openai's usage policies",
	"your request was flagged by",
	"prompt was flagged by our safety",
	"responses cannot be generated due to safety",
	"content_filter",
	"responsibleaipolicyviolation",
}

// thinkingRequiredPatterns match providers that are in thinking mode and demand
// reasoning_content be replayed on every assistant message. The remedy is to
// SEND it, never to strip it — stripping guarantees the same failure forever.
// The authoritative fix is per-model config (`thinking: true`).
var thinkingRequiredPatterns = []string{
	"must be passed back",
	"thinking mode must be passed back",
	"content[].thinking",
}

// reasoningContentPatterns match providers that do not understand the
// reasoning_content field at all. Only these warrant stripping it.
var reasoningContentPatterns = []string{
	"reasoning_content is not supported",
	"reasoning_content: unknown",
	"unknown field \"reasoning_content\"",
}

// responseFormatPatterns match providers that reject the response_format
// parameter outright. LM Studio, for example, only accepts
// {"type":"json_schema"} or {"type":"text"} and returns 400 for
// {"type":"json_object"}. Recovered by dropping response_format and
// replaying — the prompt still requests JSON and callers parse leniently.
var responseFormatPatterns = []string{
	"response_format",
	"response format",
}

var providerPolicyPatterns = []string{
	"no endpoints available matching your guardrail",
	"no endpoints available matching your data policy",
	"no endpoints found matching your data policy",
}

var timeoutPatterns = []string{
	"timed out",
	"turn timed out",
	"request timed out",
	"deadline exceeded",
	"operation timed out",
	"upstream timed out",
}

var serverDisconnectPatterns = []string{
	"server disconnected",
	"peer closed connection",
	"connection reset by peer",
	"connection was closed",
	"network connection lost",
	"unexpected eof",
	"incomplete chunked read",
}

var sslTransientPatterns = []string{
	"bad record mac",
	"ssl alert",
	"tls alert",
	"ssl handshake failure",
	"tlsv1 alert",
	"sslv3 alert",
	"bad_record_mac",
	"ssl_alert",
	"tls_alert",
	"tls_alert_internal_error",
	"[ssl:",
}

var requestValidationPatterns = []string{
	"unknown parameter",
	"unsupported parameter",
	"unrecognized request argument",
	"invalid_request_error",
	"unknown_parameter",
	"unsupported_parameter",
}

var transportErrorTypes = []string{
	"ReadTimeout", "ConnectTimeout", "PoolTimeout",
	"ConnectError", "RemoteProtocolError",
	"ConnectionError", "ConnectionResetError",
	"ConnectionAbortedError", "BrokenPipeError",
	"TimeoutError", "ReadError",
	"ServerDisconnectedError",
	"SSLError", "SSLZeroReturnError", "SSLWantReadError",
	"SSLWantWriteError", "SSLEOFError", "SSLSyscallError",
	"APIConnectionError", "APITimeoutError",
	// Go-specific transport error fragments
	"connection refused",
	"connection reset",
	"broken pipe",
	"no such host",
	"tls:",
	"i/o timeout",
	"context deadline exceeded",
}
