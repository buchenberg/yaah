package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPTool_Get(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/api/items" {
			t.Errorf("expected /api/items, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	ht := &HTTPTool{}
	result, err := ht.Execute(context.Background(), `{"url":"`+srv.URL+`/api/items"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(result, "HTTP 200") {
		t.Errorf("expected HTTP 200, got: %s", result)
	}
	if !strings.Contains(result, `{"ok":true}`) {
		t.Errorf("expected body, got: %s", result)
	}
}

func TestHTTPTool_DefaultMethodIsGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	ht := &HTTPTool{}
	_, err := ht.Execute(context.Background(), `{"url":"`+srv.URL+`"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
}

func TestHTTPTool_Post(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", ct)
		}
		body := make([]byte, 100)
		n, _ := r.Body.Read(body)
		if string(body[:n]) != `{"name":"test"}` {
			t.Errorf("unexpected body: %s", string(body[:n]))
		}
		w.WriteHeader(201)
		w.Write([]byte(`{"id":1,"name":"test"}`))
	}))
	defer srv.Close()

	ht := &HTTPTool{}
	result, err := ht.Execute(context.Background(), `{
		"method": "POST",
		"url": "`+srv.URL+`",
		"headers": {"Content-Type": "application/json"},
		"body": "{\"name\":\"test\"}"
	}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(result, "HTTP 201") {
		t.Errorf("expected HTTP 201, got: %s", result)
	}
	if !strings.Contains(result, `"name":"test"`) {
		t.Errorf("expected response body, got: %s", result)
	}
}

func TestHTTPTool_Put(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		w.WriteHeader(200)
		w.Write([]byte(`{"updated":true}`))
	}))
	defer srv.Close()

	ht := &HTTPTool{}
	_, err := ht.Execute(context.Background(), `{"method":"PUT","url":"`+srv.URL+`","body":"x"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
}

func TestHTTPTool_Patch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PATCH" {
			t.Errorf("expected PATCH, got %s", r.Method)
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	ht := &HTTPTool{}
	_, err := ht.Execute(context.Background(), `{"method":"PATCH","url":"`+srv.URL+`","body":"x"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
}

func TestHTTPTool_Delete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "DELETE" {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()

	ht := &HTTPTool{}
	result, err := ht.Execute(context.Background(), `{"method":"DELETE","url":"`+srv.URL+`"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(result, "HTTP 204") {
		t.Errorf("expected HTTP 204, got: %s", result)
	}
}

func TestHTTPTool_CustomHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Custom") != "hello" {
			t.Errorf("expected X-Custom: hello, got %s", r.Header.Get("X-Custom"))
		}
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("expected Authorization header")
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	ht := &HTTPTool{}
	_, err := ht.Execute(context.Background(), `{
		"url": "`+srv.URL+`",
		"headers": {"X-Custom": "hello", "Authorization": "Bearer secret"}
	}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
}

func TestHTTPTool_UserAgentHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua := r.Header.Get("User-Agent")
		if !strings.HasPrefix(ua, "yaah/") {
			t.Errorf("expected yaah/ User-Agent, got %s", ua)
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	ht := &HTTPTool{}
	_, err := ht.Execute(context.Background(), `{"url":"`+srv.URL+`"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
}

func TestHTTPTool_ResponseHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Rate-Limit", "100")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	ht := &HTTPTool{}
	result, err := ht.Execute(context.Background(), `{"url":"`+srv.URL+`"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(result, "X-Rate-Limit: 100") {
		t.Errorf("expected X-Rate-Limit header in output, got: %s", result)
	}
	if !strings.Contains(result, "Content-Type: application/json") {
		t.Errorf("expected Content-Type header in output, got: %s", result)
	}
}

func TestHTTPTool_ErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte(`{"error":"not found"}`))
	}))
	defer srv.Close()

	ht := &HTTPTool{}
	result, err := ht.Execute(context.Background(), `{"url":"`+srv.URL+`"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(result, "HTTP 404") {
		t.Errorf("expected HTTP 404, got: %s", result)
	}
}

func TestHTTPTool_UrlRequired(t *testing.T) {
	ht := &HTTPTool{}
	_, err := ht.Execute(context.Background(), `{}`)
	if err == nil {
		t.Fatal("expected error for empty url")
	}
	if !strings.Contains(err.Error(), "url is required") {
		t.Errorf("expected 'url is required', got: %v", err)
	}
}

func TestHTTPTool_UnsupportedMethod(t *testing.T) {
	ht := &HTTPTool{}
	_, err := ht.Execute(context.Background(), `{"method":"CONNECT","url":"http://example.com"}`)
	if err == nil {
		t.Fatal("expected error for unsupported method")
	}
}

func TestHTTPTool_InvalidJSON(t *testing.T) {
	ht := &HTTPTool{}
	_, err := ht.Execute(context.Background(), `not json`)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestHTTPTool_InvalidURL(t *testing.T) {
	ht := &HTTPTool{}
	_, err := ht.Execute(context.Background(), `{"url":"not-a-valid-url"}`)
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestHTTPTool_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep longer than the timeout.
		<-r.Context().Done()
	}))
	defer srv.Close()

	ht := &HTTPTool{}
	_, err := ht.Execute(context.Background(), `{"url":"`+srv.URL+`","timeout":1}`)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestHTTPTool_IsDangerous(t *testing.T) {
	ht := &HTTPTool{}
	tests := []struct {
		args      string
		dangerous bool
	}{
		{`{"method":"GET"}`, false},
		{`{"method":"get"}`, false},
		{`{"method":"HEAD"}`, false},
		{`{"method":"OPTIONS"}`, false},
		{`{"method":"POST"}`, true},
		{`{"method":"PUT"}`, true},
		{`{"method":"PATCH"}`, true},
		{`{"method":"DELETE"}`, true},
		{`{}`, true},                   // no method → default GET, but IsDangerous doesn't default
		{`{"method":"CONNECT"}`, true}, // unknown → dangerous
		{`not json`, false},            // invalid JSON
	}
	for _, tt := range tests {
		t.Run(tt.args, func(t *testing.T) {
			if got := ht.IsDangerous(tt.args); got != tt.dangerous {
				t.Errorf("IsDangerous(%s) = %v, want %v", tt.args, got, tt.dangerous)
			}
		})
	}
}

func TestHTTPTool_NilBodyIsFine(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	ht := &HTTPTool{}
	_, err := ht.Execute(context.Background(), `{"method":"GET","url":"`+srv.URL+`"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
}

func TestHTTPTool_LowercaseMethod(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.WriteHeader(201)
	}))
	defer srv.Close()

	ht := &HTTPTool{}
	_, err := ht.Execute(context.Background(), `{"method":"post","url":"`+srv.URL+`","body":"data"}`)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
}

func TestHTTPTool_NameDescriptionSchema(t *testing.T) {
	ht := &HTTPTool{}
	if ht.Name() != "http" {
		t.Errorf("Name() = %q, want %q", ht.Name(), "http")
	}
	if ht.Description() == "" {
		t.Error("Description() is empty")
	}
	schema := ht.Schema()
	var s map[string]any
	if err := json.Unmarshal(schema, &s); err != nil {
		t.Fatalf("Schema() is not valid JSON: %v", err)
	}
	if _, ok := s["properties"]; !ok {
		t.Error("Schema() missing properties")
	}
}
