package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// httpDefaultTimeout is the max time for a single HTTP call.
const httpDefaultTimeout = 30 * time.Second

// httpMaxResponseBody caps the response body size.
const httpMaxResponseBody = 2 << 20 // 2 MiB

// allowedHTTPMethods defines the whitelist of HTTP methods the tool supports.
// GET, HEAD, and OPTIONS are safe; mutating methods are dangerous.
var allowedHTTPMethods = map[string]struct {
	verb      string
	dangerous bool
}{
	"GET":     {"GET", false},
	"HEAD":    {"HEAD", false},
	"OPTIONS": {"OPTIONS", false},
	"POST":    {"POST", true},
	"PUT":     {"PUT", true},
	"PATCH":   {"PATCH", true},
	"DELETE":  {"DELETE", true},
}

// HTTPTool makes generic HTTP requests to interact with REST APIs.
// GET, HEAD, and OPTIONS are safe (IsDangerous returns false).
// POST, PUT, PATCH, and DELETE are dangerous and require approval.
type HTTPTool struct{}

func (t *HTTPTool) Name() string { return "http" }
func (t *HTTPTool) Description() string {
	return "Make an HTTP request to a URL. Supports GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS with custom headers and body."
}

func (t *HTTPTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"method": {
				"type": "string",
				"enum": ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"],
				"description": "HTTP method (default: GET)"
			},
			"url": {
				"type": "string",
				"description": "The URL to send the request to"
			},
			"headers": {
				"type": "object",
				"description": "HTTP headers as key-value pairs (e.g. {\"Authorization\": \"Bearer token\"})"
			},
			"body": {
				"type": "string",
				"description": "Request body as a string (for POST, PUT, PATCH)"
			},
			"timeout": {
				"type": "integer",
				"description": "Timeout in seconds (default: 30)"
			}
		},
		"required": ["url"]
	}`)
}

func (t *HTTPTool) IsDangerous(argsJSON string) bool {
	var params struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &params); err != nil {
		return false
	}
	if info, ok := allowedHTTPMethods[strings.ToUpper(params.Method)]; ok {
		return info.dangerous
	}
	// Unknown method — treat as dangerous (conservative default).
	return true
}

func (t *HTTPTool) Execute(ctx context.Context, args string) (string, error) {
	var params struct {
		Method  string            `json:"method"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
		Timeout int               `json:"timeout"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("http: invalid arguments: %w", err)
	}
	if params.URL == "" {
		return "", fmt.Errorf("http: url is required")
	}

	method := strings.ToUpper(params.Method)
	if method == "" {
		method = "GET"
	}

	if _, ok := allowedHTTPMethods[method]; !ok {
		return "", fmt.Errorf("http: unsupported method %q — allowed: GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS", method)
	}

	timeout := httpDefaultTimeout
	if params.Timeout > 0 {
		timeout = time.Duration(params.Timeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var bodyReader io.Reader
	if params.Body != "" {
		bodyReader = bytes.NewReader([]byte(params.Body))
	}

	req, err := http.NewRequestWithContext(ctx, method, params.URL, bodyReader)
	if err != nil {
		return "", fmt.Errorf("http: invalid url: %w", err)
	}

	req.Header.Set("User-Agent", "yaah/1.0")
	for k, v := range params.Headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, int64(httpMaxResponseBody)))
	if err != nil {
		return "", fmt.Errorf("http: read body: %w", err)
	}

	return formatHTTPResponse(resp, rawBody), nil
}

// formatHTTPResponse builds a human-readable response string from an
// HTTP response, including status line, sorted headers, and body.
func formatHTTPResponse(resp *http.Response, body []byte) string {
	var buf bytes.Buffer

	// Status line.
	buf.WriteString(fmt.Sprintf("HTTP %d %s\n", resp.StatusCode, resp.Status))

	// Sorted headers (skip Transfer-Encoding, it's noise).
	keys := make([]string, 0, len(resp.Header))
	for k := range resp.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.EqualFold(k, "Transfer-Encoding") {
			continue
		}
		for _, v := range resp.Header[k] {
			buf.WriteString(fmt.Sprintf("%s: %s\n", k, v))
		}
	}

	// Blank line separator, then body.
	buf.WriteByte('\n')
	result := body
	if len(result) > toolResultMaxLen {
		result = append(result[:toolResultMaxLen], []byte("\n...[body truncated]...")...)
	}
	buf.Write(result)

	return strings.TrimSpace(buf.String())
}
