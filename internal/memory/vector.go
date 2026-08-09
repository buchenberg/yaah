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
	"time"
)

// Embedding is a float32 vector produced by an embedding model.
type Embedding []float32

// Embedder produces an embedding for a single input string.
type Embedder interface {
	Embed(ctx context.Context, text string) (Embedding, error)
}

// HTTPEmbedder calls an OpenAI-compatible /v1/embeddings endpoint. It
// works with llama-server, Ollama, and provider APIs. Zero values are
// usable (http.DefaultClient, timeout defaults).
type HTTPEmbedder struct {
	// BaseURL is the embeddings endpoint base, e.g. "http://127.0.0.1:7334".
	BaseURL string
	// Client is the HTTP client used. When nil, a default client with a
	// 30-second timeout is used.
	Client *http.Client
}

// DefaultEmbeddingTimeout is the HTTP request timeout used when Client is nil.
const DefaultEmbeddingTimeout = 30 * time.Second

func (e *HTTPEmbedder) client() *http.Client {
	if e.Client != nil {
		return e.Client
	}
	return &http.Client{Timeout: DefaultEmbeddingTimeout}
}

func (e *HTTPEmbedder) url(path string) string {
	base := e.BaseURL
	for len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	return base + path
}

// Embed calls POST /v1/embeddings with the given text and returns the
// float32 embedding vector from the first data element.
func (e *HTTPEmbedder) Embed(ctx context.Context, text string) (Embedding, error) {
	body := struct {
		Model string `json:"model"`
		Input string `json:"input"`
	}{
		Model: "local",
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

// CosineSimilarity returns the cosine similarity between a and b. Both
// slices must be the same length and non-zero. Returns 0 if either
// vector has zero magnitude.
func CosineSimilarity(a, b []float32) float32 {
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
