package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/buchenberg/yaah/internal/types"
)

// StreamChunk represents a single SSE chunk from a streaming response.
type StreamChunk struct {
	ID      string         `json:"id"`
	Choices []StreamChoice `json:"choices"`
}

// StreamChoice represents a choice within a stream chunk.
type StreamChoice struct {
	Index        int         `json:"index"`
	Delta        StreamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

// StreamDelta contains the incremental content for a stream chunk.
type StreamDelta struct {
	Role      string           `json:"role,omitempty"`
	Content   string           `json:"content,omitempty"`
	ToolCalls []types.ToolCall `json:"tool_calls,omitempty"`
}

// SendStream sends a streaming chat request and returns a channel of chunks.
// The channel is closed when the stream ends or an error occurs.
// The request's Stream field is set to true (the caller's struct is mutated).
func (c *OpenAIClient) SendStream(ctx context.Context, req types.ChatRequest) (<-chan StreamChunk, <-chan error) {
	req.Stream = true

	chunks := make(chan StreamChunk, 64)
	errs := make(chan error, 1)

	go func() {
		defer close(chunks)
		defer close(errs)

		body, err := json.Marshal(req)
		if err != nil {
			errs <- fmt.Errorf("marshal request: %w", err)
			return
		}

		url := c.baseURL + "/chat/completions"
		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
		if err != nil {
			errs <- fmt.Errorf("create request: %w", err)
			return
		}

		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
		httpReq.Header.Set("Accept", "text/event-stream")

		resp, err := c.client.Do(httpReq)
		if err != nil {
			errs <- fmt.Errorf("send request: %w", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			errs <- fmt.Errorf("provider returned %d: %s", resp.StatusCode, string(respBody))
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			line := scanner.Bytes()
			chunk, done, err := parseSSEChunk(line)
			if err != nil {
				continue // skip malformed lines
			}
			if done {
				return
			}
			if chunk != nil {
				select {
				case chunks <- *chunk:
				case <-ctx.Done():
					return
				}
			}
		}

		if err := scanner.Err(); err != nil {
			errs <- fmt.Errorf("read stream: %w", err)
		}
	}()

	return chunks, errs
}

// parseSSEChunk parses a single SSE line. Returns (chunk, done, error).
// A "data: [DONE]" line returns (nil, true, nil).
// Empty lines and non-data lines return (nil, false, nil).
func parseSSEChunk(line []byte) (*StreamChunk, bool, error) {
	if len(line) == 0 {
		return nil, false, nil
	}

	// SSE format: "data: {json}" or "data: [DONE]"
	if !bytes.HasPrefix(line, []byte("data: ")) {
		return nil, false, nil
	}

	payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data: ")))
	if string(payload) == "[DONE]" {
		return nil, true, nil
	}

	var chunk StreamChunk
	if err := json.Unmarshal(payload, &chunk); err != nil {
		return nil, false, err
	}

	return &chunk, false, nil
}
