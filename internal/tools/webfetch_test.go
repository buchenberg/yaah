package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWebFetch_ErrorStatusIncludesBody pins yaah-pi21: a 404 page explains
// itself, so the status alone is not enough to tell a wrong URL from a WAF
// block or a missing route.
func TestWebFetch_ErrorStatusIncludesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("route /missing does not exist"))
	}))
	defer srv.Close()

	_, err := (&WebFetchTool{}).Execute(context.Background(), `{"url":"`+srv.URL+`/missing"}`)
	if err == nil {
		t.Fatal("a 404 must be an error")
	}
	for _, want := range []string{"404", "route /missing does not exist"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestWebFetch_ErrorStatusEmptyBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	_, err := (&WebFetchTool{}).Execute(context.Background(), `{"url":"`+srv.URL+`/private"}`)
	if err == nil {
		t.Fatal("a 403 must be an error")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "empty response body") {
		t.Errorf("unexpected error: %v", err)
	}
}
