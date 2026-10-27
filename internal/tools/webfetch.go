package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/buchenberg/yaah/internal/prompts"
)

// webFetchTimeout is the max time for a single fetch.
const webFetchTimeout = 30 * time.Second

// webFetchMaxBody caps the response body size.
const webFetchMaxBody = 2 << 20 // 2 MiB

// WebFetchTool fetches content from a URL and converts it to markdown or text.
type WebFetchTool struct{}

func (t *WebFetchTool) Name() string { return "webfetch" }
func (t *WebFetchTool) Description() string {
	return prompts.ToolDescription("webfetch")
}

func (t *WebFetchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"url": {"type": "string", "description": "The URL to fetch content from"},
			"format": {"type": "string", "enum": ["markdown", "text", "html"], "description": "Output format (default markdown)"}
		},
		"required": ["url"]
	}`)
}

type webFetchParams struct {
	URL    string `json:"url"`
	Format string `json:"format"`
}

func (t *WebFetchTool) Execute(ctx context.Context, args string) (string, error) {
	var params webFetchParams
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return "", fmt.Errorf("webfetch: invalid arguments: %w", err)
	}
	if params.URL == "" {
		return "", fmt.Errorf("webfetch: url is required")
	}
	if err := validateURL(params.URL); err != nil {
		return "", fmt.Errorf("webfetch: %w", err)
	}
	if params.Format == "" {
		params.Format = "markdown"
	}

	ctx, cancel := context.WithTimeout(ctx, webFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, params.URL, nil)
	if err != nil {
		return "", fmt.Errorf("webfetch: invalid url: %w", err)
	}
	req.Header.Set("User-Agent", "yaah/1.0")

	resp, err := toolHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("webfetch: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(webFetchMaxBody)))
	if err != nil {
		return "", fmt.Errorf("webfetch: read body: %w", err)
	}

	if resp.StatusCode >= 400 {
		// A 403/404 body usually explains itself (auth message, API error
		// JSON, route listing). Returning the status alone hides that and
		// makes wrong-URL and WAF-blocked indistinguishable.
		detail := strings.TrimSpace(truncateResult(string(body)))
		if detail == "" {
			return "", fmt.Errorf("webfetch: HTTP %s (empty response body)", resp.Status)
		}
		return "", fmt.Errorf("webfetch: HTTP %s\n%s", resp.Status, detail)
	}

	contentType := resp.Header.Get("Content-Type")

	switch params.Format {
	case "html":
		return truncateResult(string(body)), nil
	case "text":
		return truncateResult(htmlToText(string(body))), nil
	default:
		if strings.Contains(contentType, "text/html") {
			return truncateResult(htmlToText(string(body))), nil
		}
		return truncateResult(string(body)), nil
	}
}

var (
	htmlTagRe      = regexp.MustCompile(`<[^>]*>`)
	htmlScriptRe   = regexp.MustCompile(`(?s)<script[^>]*>.*?</script>`)
	htmlStyleRe    = regexp.MustCompile(`(?s)<style[^>]*>.*?</style>`)
	htmlEntityRe   = regexp.MustCompile(`&[a-zA-Z]+;|&#\d+;|&#x[0-9a-fA-F]+;`)
	multiNewlineRe = regexp.MustCompile(`\n{3,}`)
)

// htmlToText strips HTML tags and returns plain text.
func htmlToText(html string) string {
	html = htmlScriptRe.ReplaceAllString(html, "")
	html = htmlStyleRe.ReplaceAllString(html, "")

	var buf bytes.Buffer
	parts := htmlTagRe.Split(html, -1)
	for i, part := range parts {
		if i > 0 {
			buf.WriteByte('\n')
		}
		decoded := decodeHTMLEntities(part)
		decoded = strings.TrimSpace(decoded)
		if decoded != "" {
			buf.WriteString(decoded)
		}
	}

	result := multiNewlineRe.ReplaceAllString(buf.String(), "\n\n")
	return strings.TrimSpace(result)
}

func decodeHTMLEntities(s string) string {
	return htmlEntityRe.ReplaceAllStringFunc(s, func(e string) string {
		switch e {
		case "&amp;":
			return "&"
		case "&lt;":
			return "<"
		case "&gt;":
			return ">"
		case "&quot;":
			return `"`
		case "&apos;":
			return "'"
		case "&nbsp;":
			return " "
		default:
			return e
		}
	})
}
