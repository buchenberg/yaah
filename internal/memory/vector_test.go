package memory

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	orig := Embedding{0.1, 0.2, -0.3, 1.0, 0.0}
	b := EncodeEmbedding(orig)
	got := DecodeEmbedding(b)
	if len(got) != len(orig) {
		t.Fatalf("len = %d, want %d", len(got), len(orig))
	}
	for i := range orig {
		if math.Abs(float64(got[i]-orig[i])) > 1e-6 {
			t.Errorf("[%d] = %f, want %f", i, got[i], orig[i])
		}
	}
}

func TestDecodeEmbeddingEmpty(t *testing.T) {
	got := DecodeEmbedding(nil)
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}

func TestCosineSimilarityIdentical(t *testing.T) {
	v := Embedding{1, 2, 3}
	s := float64(CosineSimilarity(v, v))
	if math.Abs(s-1.0) > 1e-4 {
		t.Errorf("CosineSimilarity(identical) = %f, want 1.0", s)
	}
}

func TestCosineSimilarityOrthogonal(t *testing.T) {
	a := Embedding{1, 0, 0}
	b := Embedding{0, 1, 0}
	s := CosineSimilarity(a, b)
	if s != 0 {
		t.Errorf("CosineSimilarity(orthogonal) = %f, want 0", s)
	}
}

func TestCosineSimilarityZeroVector(t *testing.T) {
	a := Embedding{0, 0, 0}
	b := Embedding{1, 2, 3}
	s := CosineSimilarity(a, b)
	if s != 0 {
		t.Errorf("CosineSimilarity(zero, nonzero) = %f, want 0", s)
	}
}

func TestCosineSimilarityMismatchedLength(t *testing.T) {
	defer func() {
		// CosineSimilarity does index-out-of-range if lengths differ; this
		// is a programming error (caller's responsibility), not a recoverable
		// condition.
		if r := recover(); r != nil {
			t.Log("expected panic on mismatched lengths:", r)
		} else {
			t.Error("expected panic")
		}
	}()
	CosineSimilarity(Embedding{1, 2}, Embedding{1})
}

func TestEncodeEmbeddingDeterministic(t *testing.T) {
	e := Embedding{1.0, -1.0, 0.5}
	b1 := EncodeEmbedding(e)
	b2 := EncodeEmbedding(e)
	if string(b1) != string(b2) {
		t.Error("EncodeEmbedding not deterministic")
	}
}

func TestHTTPEmbedderEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("path = %s, want /v1/embeddings", r.URL.Path)
		}

		var body struct {
			Model string `json:"model"`
			Input string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body.Input == "" {
			t.Error("input is empty")
		}

		resp := struct {
			Data []struct {
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
		}{
			Data: []struct {
				Embedding []float64 `json:"embedding"`
			}{
				{Embedding: []float64{0.1, 0.2, -0.3, 1.0}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e := &HTTPEmbedder{BaseURL: srv.URL}
	emb, err := e.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(emb) != 4 {
		t.Fatalf("len = %d, want 4", len(emb))
	}
	want := Embedding{0.1, 0.2, -0.3, 1.0}
	for i := range want {
		if math.Abs(float64(emb[i]-want[i])) > 1e-4 {
			t.Errorf("[%d] = %f, want %f", i, emb[i], want[i])
		}
	}
}

func TestHTTPEmbedderEmbedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"something went wrong"}`))
	}))
	defer srv.Close()

	e := &HTTPEmbedder{BaseURL: srv.URL}
	_, err := e.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error from 500 response")
	}
}

func TestHTTPEmbedderEmbedEmptyData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": []any{},
		})
	}))
	defer srv.Close()

	e := &HTTPEmbedder{BaseURL: srv.URL}
	_, err := e.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for empty data response")
	}
}

func TestHTTPEmbedderURL(t *testing.T) {
	e := &HTTPEmbedder{BaseURL: "http://localhost:7334/"}
	if got := e.url("/v1/embeddings"); got != "http://localhost:7334/v1/embeddings" {
		t.Errorf("url = %q", got)
	}
	e2 := &HTTPEmbedder{BaseURL: "http://localhost:7334"}
	if got := e2.url("/v1/embeddings"); got != "http://localhost:7334/v1/embeddings" {
		t.Errorf("url = %q", got)
	}
	// Provider-style base URLs with trailing /v1 (e.g. LM Studio, ollama)
	e3 := &HTTPEmbedder{BaseURL: "http://localhost:1234/v1"}
	if got := e3.url("/v1/embeddings"); got != "http://localhost:1234/v1/embeddings" {
		t.Errorf("url with trailing /v1 = %q, want no double /v1", got)
	}
	e4 := &HTTPEmbedder{BaseURL: "http://localhost:1234/v1/"}
	if got := e4.url("/v1/embeddings"); got != "http://localhost:1234/v1/embeddings" {
		t.Errorf("url with trailing /v1/ = %q", got)
	}
}

func TestHTTPEmbedderContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until cancelled
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	e := &HTTPEmbedder{BaseURL: srv.URL}
	_, err := e.Embed(ctx, "test")
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestNewEmbedder(t *testing.T) {
	e := NewEmbedder("http://localhost:7334", "", nil)
	if e == nil {
		t.Fatal("NewEmbedder returned nil")
	}
	if e.BaseURL != "http://localhost:7334" {
		t.Errorf("BaseURL = %q", e.BaseURL)
	}
	if e.Client != nil {
		t.Error("expected nil Client when none provided")
	}
	if e.Model != "local" {
		t.Errorf("Model = %q, want local", e.Model)
	}
}

func TestNewEmbedderWithClient(t *testing.T) {
	c := &http.Client{}
	e := NewEmbedder("http://localhost:7335", "custom-model", c)
	if e.Client != c {
		t.Error("expected provided client")
	}
}

func TestHTTPEmbedderClientDefault(t *testing.T) {
	e := &HTTPEmbedder{}
	c := e.client()
	if c == nil {
		t.Fatal("expected default client")
	}
	if c.Timeout != DefaultEmbeddingTimeout {
		t.Errorf("timeout = %v, want %v", c.Timeout, DefaultEmbeddingTimeout)
	}
}

func TestHTTPEmbedderClientExplicit(t *testing.T) {
	e := &HTTPEmbedder{Client: &http.Client{}}
	c := e.client()
	if c != e.Client {
		t.Error("expected explicit client")
	}
}

func TestHTTPEmbedderEmbedMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	e := &HTTPEmbedder{BaseURL: srv.URL}
	_, err := e.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected decode error")
	}
}

func TestHTTPEmbedderEmbedEmptyEmbedding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float64{}}},
		})
	}))
	defer srv.Close()

	e := &HTTPEmbedder{BaseURL: srv.URL}
	_, err := e.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error for zero-length embedding")
	}
}
