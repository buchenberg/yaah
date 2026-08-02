// Package providers implements model provider clients for yaah.
// The primary client targets the OpenAI Chat Completions API, which
// is supported (natively or via compatibility proxies) by OpenAI,
// Anthropic, Google, OpenRouter, Ollama, LM Studio, and vLLM.
package providers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/types"
)

// OpenAIClient sends chat completion requests to an OpenAI-compatible endpoint.
type OpenAIClient struct {
	baseURL string
	apiKey  string
	client  *http.Client

	// ThinkingOverrides holds per-model thinking-mode overrides from config.
	// When a model has an entry, that value wins over auto-detection.
	ThinkingOverrides map[string]*bool

	// ExtraHeaders are additional headers applied to every request.
	// Used by the Copilot client for API version, user-agent, etc.
	ExtraHeaders map[string]string
}

// NewOpenAIClient creates a new client targeting baseURL (e.g. "https://api.openai.com").
// timeoutSeconds is the HTTP client timeout; 0 means no timeout (useful for local
// models behind slow servers like llama.cpp). Negative values fall back to 120s.
func NewOpenAIClient(baseURL, apiKey string, timeoutSeconds int) *OpenAIClient {
	to := time.Duration(timeoutSeconds) * time.Second
	if timeoutSeconds < 0 {
		to = 120 * time.Second
	}
	return &OpenAIClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		client: &http.Client{
			Timeout: to,
		},
	}
}

// resolveThinkingMode determines whether reasoning_content should always be
// serialized on assistant messages. Resolution order:
//  1. Per-model config override (ThinkingOverrides map)
//  2. Auto-detection from the known thinking-model registry
func (c *OpenAIClient) resolveThinkingMode(model string) bool {
	name := sanitizeModelName(model)
	if c.ThinkingOverrides != nil {
		if v, ok := c.ThinkingOverrides[name]; ok && v != nil {
			return *v
		}
	}
	return IsThinkingModel(model)
}

// Send posts a ChatRequest to the provider and returns the parsed ChatResponse.
func (c *OpenAIClient) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	body, err := json.Marshal(lowerRequest(req, c.resolveThinkingMode(req.Model)))
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := c.baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	for k, v := range c.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}
	setSessionHeaders(httpReq, SessionIDFromContext(ctx))

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("provider returned %d: %s  [msgs=%d model=%s]", resp.StatusCode, strings.TrimSpace(string(respBody)), len(req.Messages), req.Model)
	}

	var result types.ChatResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &result, nil
}

// ListModels fetches the available model IDs from the provider's /v1/models endpoint.
func (c *OpenAIClient) ListModels(ctx context.Context) ([]string, error) {
	url := c.baseURL + "/models"
	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	for k, v := range c.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list models returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}

	models := make([]string, 0, len(result.Data))
	for _, m := range result.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	return models, nil
}

// sessionIDKey is the context key for the session ID used in affinity headers.
type sessionIDKey struct{}

// SessionIDFromContext extracts the session ID from a context, or returns ""
// if none is present.
func SessionIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(sessionIDKey{}).(string); ok {
		return v
	}
	return ""
}

// WithSessionID returns a child context with the session ID set.
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	if sessionID == "" {
		return ctx
	}
	return context.WithValue(ctx, sessionIDKey{}, sessionID)
}

// setSessionHeaders adds session-affinity headers to an HTTP request.
// Uses SHA-256 to produce a stable, fixed-length header value from the session ID.
func setSessionHeaders(req *http.Request, sessionID string) {
	if sessionID == "" {
		return
	}
	hash := sha256.Sum256([]byte(sessionID))
	hexID := hex.EncodeToString(hash[:])
	req.Header.Set("x-session-id", hexID)
	req.Header.Set("x-session-affinity", hexID)
}
