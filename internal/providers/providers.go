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
	"time"

	"github.com/buchenberg/yaah/internal/types"
)

// OpenAIClient sends chat completion requests to an OpenAI-compatible endpoint.
type OpenAIClient struct {
	baseURL string
	apiKey  string
	client  *http.Client
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

// Send posts a ChatRequest to the provider and returns the parsed ChatResponse.
func (c *OpenAIClient) Send(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	body, err := json.Marshal(req)
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
		return nil, fmt.Errorf("provider returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result types.ChatResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return &result, nil
}

// ModelLister is an optional interface for providers that can list available models.
type ModelLister interface {
	ListModels(ctx context.Context) ([]string, error)
}

// ListModels fetches the available model IDs from the provider's /v1/models endpoint.
func (c *OpenAIClient) ListModels(ctx context.Context) ([]string, error) {
	url := c.baseURL + "/models"
	httpReq, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

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

// EstimateTokens returns a rough token count using the char/4 heuristic.
// This is a fallback; when a proper tokenizer is available (e.g. tiktoken),
// it will be used instead. Accurate to within ~20% for English text.
func EstimateTokens(text string) int {
	// Rough estimate: 1 token ≈ 4 characters for English text
	n := len(text)
	if n == 0 {
		return 0
	}
	return (n + 3) / 4
}
