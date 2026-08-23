package errorclassify

import (
	"errors"
	"fmt"
	"testing"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		meta     ErrorMeta
		want     ErrorReason
		wantHint string // which recovery hint should be set
	}{
		{
			name: "context overflow — max tokens",
			err:  errors.New("This request exceeds the maximum number of tokens allowed"),
			meta: ErrorMeta{StatusCode: 400, NumMessages: 10},
			want: ReasonContextOverflow, wantHint: "compress",
		},
		{
			name: "context overflow — context length",
			err:  errors.New("context length exceeded: 200k tokens, max is 128k"),
			meta: ErrorMeta{StatusCode: 400},
			want: ReasonContextOverflow, wantHint: "compress",
		},
		{
			name: "context overflow — Ollama",
			err:  errors.New("slot context: 4096 tokens, prompt 8192 tokens"),
			meta: ErrorMeta{StatusCode: 500},
			want: ReasonContextOverflow, wantHint: "compress",
		},
		{
			name: "billing — insufficient credits",
			err:  errors.New("You have insufficient credits to use this model"),
			meta: ErrorMeta{StatusCode: 402},
			want: ReasonBilling, wantHint: "rotate",
		},
		{
			name: "billing — quota exceeded (status 429 → rate_limit)",
			err:  errors.New("You have exceeded your current quota"),
			meta: ErrorMeta{StatusCode: 429},
			want: ReasonRateLimit, wantHint: "rotate",
		},
		{
			name: "rate limit — 429",
			err:  errors.New("Too many requests"),
			meta: ErrorMeta{StatusCode: 429},
			want: ReasonRateLimit, wantHint: "rotate",
		},
		{
			name: "rate limit — message pattern",
			err:  errors.New("you have hit your rate limit. try again in 30s"),
			meta: ErrorMeta{StatusCode: 200},
			want: ReasonRateLimit, wantHint: "rotate",
		},
		{
			name: "auth — 401",
			err:  errors.New("Unauthorized"),
			meta: ErrorMeta{StatusCode: 401},
			want: ReasonAuth, wantHint: "rotate",
		},
		{
			name: "auth — 403",
			err:  errors.New("Forbidden"),
			meta: ErrorMeta{StatusCode: 403},
			want: ReasonAuth, wantHint: "rotate",
		},
		{
			name: "auth — invalid key message",
			err:  errors.New("invalid_api_key: the api key is not valid"),
			meta: ErrorMeta{StatusCode: 200},
			want: ReasonAuth, wantHint: "rotate",
		},
		{
			name: "server error — 500",
			err:  errors.New("Internal Server Error"),
			meta: ErrorMeta{StatusCode: 500},
			want: ReasonServerError, wantHint: "retry",
		},
		{
			name: "server error — 502",
			err:  errors.New("Bad Gateway"),
			meta: ErrorMeta{StatusCode: 502},
			want: ReasonServerError, wantHint: "retry",
		},
		{
			name: "overloaded — 503",
			err:  errors.New("Service Unavailable"),
			meta: ErrorMeta{StatusCode: 503},
			want: ReasonOverloaded, wantHint: "retry",
		},
		{
			name: "overloaded — 529",
			err:  errors.New("overloaded"),
			meta: ErrorMeta{StatusCode: 529},
			want: ReasonOverloaded, wantHint: "retry",
		},
		{
			name: "model not found — 404",
			err:  errors.New("model not found: gpt-5"),
			meta: ErrorMeta{StatusCode: 404},
			want: ReasonModelNotFound, wantHint: "rotate",
		},
		{
			name: "model not found — message only",
			err:  errors.New("The model 'bogus' does not exist"),
			meta: ErrorMeta{StatusCode: 200},
			want: ReasonModelNotFound, wantHint: "rotate",
		},
		{
			name: "payload too large — 413",
			err:  errors.New("Request Entity Too Large"),
			meta: ErrorMeta{StatusCode: 413},
			want: ReasonPayloadTooLarge, wantHint: "compress",
		},
		{
			name: "format error — 400",
			err:  errors.New("unknown parameter: top_k"),
			meta: ErrorMeta{StatusCode: 400},
			want: ReasonFormatError, wantHint: "abort",
		},
		{
			name: "content policy — OpenAI",
			err:  errors.New("Your request violates our usage policies"),
			meta: ErrorMeta{StatusCode: 400},
			want: ReasonContentPolicyBlocked, wantHint: "abort",
		},
		{
			name: "content policy — Anthropic safety",
			err:  errors.New("prompt was flagged by our safety system"),
			meta: ErrorMeta{StatusCode: 200},
			want: ReasonContentPolicyBlocked, wantHint: "abort",
		},
		{
			name: "provider policy — OpenRouter",
			err:  errors.New("no endpoints available matching your data policy"),
			meta: ErrorMeta{StatusCode: 200},
			want: ReasonProviderPolicyBlocked, wantHint: "abort",
		},
		{
			name: "timeout — message",
			err:  errors.New("request timed out after 30s"),
			meta: ErrorMeta{StatusCode: 0},
			want: ReasonTimeout, wantHint: "retry",
		},
		{
			name: "timeout — context deadline",
			err:  errors.New("context deadline exceeded"),
			meta: ErrorMeta{StatusCode: 0},
			want: ReasonTimeout, wantHint: "retry",
		},
		{
			name: "server disconnect — large session",
			err:  errors.New("connection reset by peer"),
			meta: ErrorMeta{StatusCode: 0, NumMessages: 30},
			want: ReasonContextOverflow, wantHint: "compress",
		},
		{
			name: "server disconnect — small session",
			err:  errors.New("connection reset by peer"),
			meta: ErrorMeta{StatusCode: 0, NumMessages: 5},
			want: ReasonTimeout, wantHint: "retry",
		},
		{
			name: "nil error",
			err:  nil,
			meta: ErrorMeta{},
			want: ReasonUnknown, wantHint: "",
		},
		{
			name: "billing — OpenRouter spending limit 403",
			err:  errors.New("spending limit exceeded"),
			meta: ErrorMeta{StatusCode: 403},
			want: ReasonBilling, wantHint: "rotate",
		},
		{
			name: "billing — Nous 404 credit depletion",
			err:  errors.New("model_not_supported_on_free_tier: please upgrade"),
			meta: ErrorMeta{StatusCode: 404},
			want: ReasonBilling, wantHint: "rotate",
		},
		{
			name: "context overflow — AWS Bedrock",
			err:  errors.New("exceeds the maximum number of input tokens"),
			meta: ErrorMeta{StatusCode: 400},
			want: ReasonContextOverflow, wantHint: "compress",
		},
		{
			name: "transient usage limit — 402",
			err:  errors.New("usage limit exceeded. try again in 5 minutes"),
			meta: ErrorMeta{StatusCode: 402},
			want: ReasonRateLimit, wantHint: "rotate",
		},
		{
			name: "request validation — 502 from gateway",
			err:  errors.New("unknown_parameter: top_p is not supported"),
			meta: ErrorMeta{StatusCode: 502},
			want: ReasonFormatError, wantHint: "abort",
		},
		{
			name: "SSLError transport",
			err:  errors.New("SSLError: bad record mac"),
			meta: ErrorMeta{StatusCode: 0},
			want: ReasonTimeout, wantHint: "retry",
		},
		{
			name: "other 4xx — 422",
			err:  errors.New("Unprocessable Entity"),
			meta: ErrorMeta{StatusCode: 422},
			want: ReasonFormatError, wantHint: "abort",
		},
		{
			name: "other 5xx — 504",
			err:  errors.New("Gateway Timeout"),
			meta: ErrorMeta{StatusCode: 504},
			want: ReasonServerError, wantHint: "retry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Classify(tt.err, tt.meta)
			if c.Reason != tt.want {
				t.Errorf("reason = %v, want %v", c.Reason, tt.want)
			}
			switch tt.wantHint {
			case "compress":
				if !c.ShouldCompress {
					t.Error("ShouldCompress not set")
				}
			case "rotate":
				if !c.ShouldRotateCred {
					t.Error("ShouldRotateCred not set")
				}
			case "abort":
				if !c.ShouldAbort {
					t.Error("ShouldAbort not set")
				}
			case "retry":
				if !c.Retryable {
					t.Error("Retryable not set")
				}
			case "":
				// no specific hint expected
			}
		})
	}
}

func TestClassify_priority(t *testing.T) {
	// Status code beats message pattern: 429 overrides any billing text in body
	err := errors.New("insufficient credits for model. rate limit exceeded")
	c := Classify(err, ErrorMeta{StatusCode: 429})
	if c.Reason != ReasonRateLimit {
		t.Errorf("status 429 should win over billing pattern, got %v", c.Reason)
	}

	// Context overflow takes priority over generic 400
	err = errors.New("request exceeds the maximum context length of 128k tokens")
	c = Classify(err, ErrorMeta{StatusCode: 400})
	if c.Reason != ReasonContextOverflow {
		t.Errorf("context overflow pattern should win over generic 400, got %v", c.Reason)
	}
}

func TestIsContextOverflow(t *testing.T) {
	err := errors.New("context length exceeded")
	if !IsContextOverflow(err, ErrorMeta{}) {
		t.Error("expected IsContextOverflow = true")
	}
	err = errors.New("random error")
	if IsContextOverflow(err, ErrorMeta{}) {
		t.Error("expected IsContextOverflow = false")
	}
	err = nil
	if IsContextOverflow(err, ErrorMeta{}) {
		t.Error("expected IsContextOverflow = false for nil")
	}
}

func TestErrorReasonString(t *testing.T) {
	tests := map[ErrorReason]string{
		ReasonUnknown:               "unknown",
		ReasonAuth:                  "auth",
		ReasonAuthPermanent:         "auth_permanent",
		ReasonBilling:               "billing",
		ReasonRateLimit:             "rate_limit",
		ReasonOverloaded:            "overloaded",
		ReasonServerError:           "server_error",
		ReasonTimeout:               "timeout",
		ReasonContextOverflow:       "context_overflow",
		ReasonPayloadTooLarge:       "payload_too_large",
		ReasonModelNotFound:         "model_not_found",
		ReasonContentPolicyBlocked:  "content_policy_blocked",
		ReasonProviderPolicyBlocked: "provider_policy_blocked",
		ReasonFormatError:           "format_error",
	}
	for reason, want := range tests {
		got := reason.String()
		if got != want {
			t.Errorf("ErrorReason(%d).String() = %q, want %q", reason, got, want)
		}
	}
}

func TestClassifiedError_Error(t *testing.T) {
	ce := ClassifiedError{Reason: ReasonBilling, Message: "out of credits"}
	if ce.Error() != "billing: out of credits" {
		t.Errorf("Error() = %q, want %q", ce.Error(), "billing: out of credits")
	}
}

// fakeAPIError mirrors providers.APIError's typed surface without
// importing it — this package stays dependency-free.
type fakeAPIError struct {
	status int
	code   string
}

func (e *fakeAPIError) Error() string        { return "provider returned trouble" }
func (e *fakeAPIError) HTTPStatus() int      { return e.status }
func (e *fakeAPIError) ProviderCode() string { return e.code }

// TestClassify_typedStatusBeatsStringParsing verifies a structured error
// is classified by its HTTP status even when the message text would
// classify differently (or not at all).
func TestClassify_typedStatusBeatsStringParsing(t *testing.T) {
	// Message text says nothing classifiable; the typed 401 must win.
	err := &fakeAPIError{status: 401, code: "invalid_api_key"}
	c := Classify(err, ErrorMeta{})
	if c.Reason != ReasonAuth {
		t.Errorf("Reason = %v, want auth", c.Reason)
	}
	if !c.ShouldRotateCred {
		t.Error("expected ShouldRotateCred for typed 401")
	}

	// Wrapped typed errors must be found through errors.As.
	wrapped := fmt.Errorf("call failed: %w", &fakeAPIError{status: 429})
	c = Classify(wrapped, ErrorMeta{})
	if c.Reason != ReasonRateLimit {
		t.Errorf("Reason = %v, want rate_limit", c.Reason)
	}
}

// TestClassify_providerCodeIsSearchable verifies a structured code that
// previously only lived inside raw response bodies still drives
// classification when the rendered message omits it.
func TestClassify_providerCodeIsSearchable(t *testing.T) {
	// Status 0 (transport-level): message patterns decide, so the
	// appended provider code must be searchable.
	err := &fakeAPIError{status: 0, code: "insufficient_quota"}
	c := Classify(err, ErrorMeta{})
	if c.Reason != ReasonBilling {
		t.Errorf("Reason = %v, want billing from provider code", c.Reason)
	}
}
