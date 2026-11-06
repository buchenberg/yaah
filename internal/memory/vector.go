package memory

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// Embedding is a float32 vector produced by an embedding model.
type Embedding []float32

// Embedder produces an embedding for a single input string.
type Embedder interface {
	Embed(ctx context.Context, text string) (Embedding, error)
}

// QueryEmbedder is an optional interface for embedders that treat query
// text differently from document text (e.g. nomic-style search_query:/
// search_document: prefixes). Callers embedding a search query should use
// EmbedQuery, which falls back to Embed for plain embedders.
type QueryEmbedder interface {
	Embedder
	EmbedQuery(ctx context.Context, text string) (Embedding, error)
}

// EmbedQuery embeds search/query text with the embedder's query-side
// treatment when it has one, falling back to Embed otherwise. Callers
// outside this package (e.g. skill search) use it to embed a query without
// reaching for Embed, which carries the document-side prefix.
func EmbedQuery(ctx context.Context, e Embedder, text string) (Embedding, error) {
	if q, ok := e.(QueryEmbedder); ok {
		return q.EmbedQuery(ctx, text)
	}
	return e.Embed(ctx, text)
}

// HTTPEmbedder calls an OpenAI-compatible /v1/embeddings endpoint. It
// works with llama-server, Ollama, and provider APIs. Zero values are
// usable (http.DefaultClient, timeout defaults).
type HTTPEmbedder struct {
	// BaseURL is the embeddings endpoint base, e.g. "http://127.0.0.1:7334".
	BaseURL string
	// APIKey, when non-empty, is sent as a Bearer token. Local servers
	// such as llama-server do not need it.
	APIKey string
	// Model is the model name sent to the embeddings endpoint.
	// Defaults to "local" (works for llama-server). Set to an ollama model
	// name (e.g. "nomic-embed-text:latest") for ollama.
	Model string
	// Client is the HTTP client used. When nil, a default client with a
	// 30-second timeout is used.
	Client *http.Client
	// QueryPrefix and DocumentPrefix are optional asymmetric prefixes
	// applied to search-query and document text respectively (nomic-style
	// "search_query: " / "search_document: "). Empty means no prefix
	// (symmetric models), matching the previous behavior.
	QueryPrefix    string
	DocumentPrefix string
}

// DefaultEmbeddingTimeout is the HTTP request timeout used when Client is nil.
const DefaultEmbeddingTimeout = 30 * time.Second

// NewEmbedder creates an HTTPEmbedder for the given base URL, API key,
// model name, and HTTP client. When apiKey is empty, no Authorization
// header is sent (local servers). When model is empty, defaults to
// "local". When client is nil, a default client with a 30-second
// timeout is used.
func NewEmbedder(baseURL, apiKey, model string, client *http.Client) *HTTPEmbedder {
	if model == "" {
		model = "local"
	}
	return &HTTPEmbedder{BaseURL: baseURL, APIKey: apiKey, Model: model, Client: client}
}

func (e *HTTPEmbedder) client() *http.Client {
	if e.Client != nil {
		return e.Client
	}
	return &http.Client{Timeout: DefaultEmbeddingTimeout}
}

func (e *HTTPEmbedder) model() string {
	if e.Model == "" {
		return "local"
	}
	return e.Model
}

func (e *HTTPEmbedder) url(path string) string {
	base := e.BaseURL
	for len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	// Strip trailing /v1 so we don't double it when appending /v1/embeddings.
	base = strings.TrimSuffix(base, "/v1")
	return base + path
}

// Embed embeds document text, applying DocumentPrefix when set. It calls
// POST /v1/embeddings and returns the float32 embedding vector from the
// first data element.
func (e *HTTPEmbedder) Embed(ctx context.Context, text string) (Embedding, error) {
	return e.embed(ctx, e.DocumentPrefix+text)
}

// EmbedQuery embeds search-query text, applying QueryPrefix when set. It
// satisfies QueryEmbedder, so search paths prefer it over Embed for
// nomic-style asymmetric prefixes.
func (e *HTTPEmbedder) EmbedQuery(ctx context.Context, text string) (Embedding, error) {
	return e.embed(ctx, e.QueryPrefix+text)
}

// embed POSTs /v1/embeddings with the given (already-prefixed) text and
// returns the float32 embedding vector from the first data element.
func (e *HTTPEmbedder) embed(ctx context.Context, text string) (Embedding, error) {
	body := struct {
		Model string `json:"model"`
		Input string `json:"input"`
	}{
		Model: e.model(),
		Input: text,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("embed: marshal: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url("/v1/embeddings"), bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("embed: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.APIKey)
	}

	resp, err := e.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("embed: %s (status %d)", string(msg), resp.StatusCode)
	}

	type embResp struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	var r embResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("embed: decode: %w", err)
	}
	if len(r.Data) == 0 || len(r.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("embed: empty response")
	}

	emb := make(Embedding, len(r.Data[0].Embedding))
	for i, v := range r.Data[0].Embedding {
		emb[i] = float32(v)
	}
	return emb, nil
}

// CosineSimilarity returns the cosine similarity between a and b. Returns
// 0 if the lengths differ or if either vector has zero magnitude.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float32
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB))))
}

// EncodeEmbedding serializes an Embedding to a little-endian float32 byte
// slice for SQLite BLOB storage.
func EncodeEmbedding(e Embedding) []byte {
	buf := make([]byte, 4*len(e))
	for i, v := range e {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(v))
	}
	return buf
}

// DecodeEmbedding deserializes a BLOB back into an Embedding.
func DecodeEmbedding(b []byte) Embedding {
	e := make(Embedding, len(b)/4)
	for i := range e {
		e[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return e
}
