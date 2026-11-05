package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/buchenberg/yaah/internal/types"
)

// The streaming wire shapes are defined in internal/types (stream.go there):
// the engine's LLM layer consumes a stream without importing this package, so
// the type must not live here (yaah-9RCG). These aliases stay as the
// provider-side spelling of the same types.
type (
	StreamChunk  = types.StreamChunk
	StreamChoice = types.StreamChoice
	StreamDelta  = types.StreamDelta
)

// SendStream sends a streaming chat request and returns a channel of chunks.
// The channel is closed when the stream ends or an error occurs.
// The request's Stream field is set to true (the caller's struct is mutated).
func (c *OpenAIClient) SendStream(ctx context.Context, req types.ChatRequest) (<-chan StreamChunk, <-chan error) {
	req.Stream = true
	req.StreamOptions = &types.StreamOptions{IncludeUsage: true}
	c.applyDefaultMaxTokens(&req)

	chunks := make(chan StreamChunk, 64)
	errs := make(chan error, 1)

	go func() {
		defer close(chunks)
		defer close(errs)

		body, err := json.Marshal(lowerRequest(req, c.lowerOptions(req.Model)))
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
		for k, v := range c.ExtraHeaders {
			httpReq.Header.Set(k, v)
		}
		setSessionHeaders(httpReq, SessionIDFromContext(ctx))

		resp, err := c.client.Do(httpReq)
		if err != nil {
			errs <- fmt.Errorf("send request: %w", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			var roles []string
			for _, m := range req.Messages {
				roles = append(roles, m.Role)
			}
			rolesStr := strings.Join(roles, ",")
			errs <- newAPIError(resp.StatusCode, respBody).
				withRequestDetail(len(req.Messages), rolesStr, req.Model)
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		loggedUndecodable := false
		loggedContentShape := false
		for scanner.Scan() {
			line := scanner.Bytes()
			chunk, done, err := parseSSEChunk(line)
			if err != nil {
				// A chunk that does not decode is otherwise invisible: the
				// turn keeps streaming and simply loses whatever it carried
				// (typically all of the reasoning). Warn once per stream so a
				// wire-shape change stays diagnosable without flooding the log.
				if !loggedUndecodable {
					loggedUndecodable = true
					// Only the error and the size are logged: the line itself is
					// model output, which must not land in a log file.
					slog.Warn("provider stream: dropping undecodable chunk; further occurrences in this turn are silent",
						"model", req.Model, "error", err, "bytes", len(line))
				}
				continue // skip malformed lines
			}
			if done {
				return
			}
			if chunk != nil {
				// An unreadable content shape degrades the delta to empty
				// content (see StreamDelta.UnmarshalJSON) but must not be
				// silent: the same once-per-stream rule as undecodable chunks.
				if !loggedContentShape {
					for _, choice := range chunk.Choices {
						if choice.Delta.ContentShapeErr() != nil {
							loggedContentShape = true
							// Only the error is logged: the payload is model
							// output, which must not land in a log file.
							slog.Warn("provider stream: unreadable delta content shape; the delta's content was dropped, its tool calls kept. further occurrences in this turn are silent",
								"model", req.Model, "error", choice.Delta.ContentShapeErr())
							break
						}
					}
				}
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
	if chunk.Usage != nil {
		// OpenAI-compatible prompt_tokens include cache reads.
		chunk.Usage.PromptIncludesCache = true
	}

	return &chunk, false, nil
}
