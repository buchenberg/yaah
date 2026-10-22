package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/memory"
)

func openScopeTestDB(t *testing.T) *memory.DB {
	t.Helper()
	db, err := memory.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestMemoryAddTool_Scope(t *testing.T) {
	cases := []struct {
		name      string
		toolScope string
		args      string
		want      string
		wantErr   bool
	}{
		{"call scope wins", memory.ScopeGlobal, `{"text":"call scope atom","scope":"session"}`, memory.ScopeSession, false},
		{"tool default applies", memory.ScopeSession, `{"text":"tool default atom"}`, memory.ScopeSession, false},
		{"global fallback", "", `{"text":"fallback atom"}`, memory.ScopeGlobal, false},
		{"unknown scope rejected", "", `{"text":"bad atom","scope":"project"}`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openScopeTestDB(t)
			tool := &MemoryAddTool{DB: db, Scope: tc.toolScope}
			_, err := tool.Execute(context.Background(), tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Execute err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			entries, err := db.ListMemoryScoped(10, "")
			if err != nil {
				t.Fatalf("ListMemoryScoped: %v", err)
			}
			if len(entries) != 1 || entries[0].Scope != tc.want {
				t.Fatalf("entries = %+v, want one row scoped %q", entries, tc.want)
			}
		})
	}
}

func TestMemoryAddTool_ScopeSchema(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal((&MemoryAddTool{}).Schema(), &schema); err != nil {
		t.Fatalf("Schema is not valid JSON: %v", err)
	}
	scope, ok := schema.Properties["scope"]
	if !ok {
		t.Fatal("memory_add schema has no scope property")
	}
	got := map[string]bool{}
	for _, v := range scope.Enum {
		got[v] = true
	}
	for _, want := range []string{memory.ScopeSession, memory.ScopeGlobal} {
		if !got[want] {
			t.Errorf("scope enum missing %q", want)
		}
	}
}

func TestMemorySearchTool_Scopes(t *testing.T) {
	db := openScopeTestDB(t)
	now := time.Now().Unix()
	if err := db.AddMemory(memory.Entry{ID: "g1", Text: "global fact", Scope: memory.ScopeGlobal, CreatedAt: now}); err != nil {
		t.Fatalf("AddMemory(global): %v", err)
	}
	if err := db.AddMemory(memory.Entry{ID: "s1", Text: "session fact", Scope: memory.ScopeSession, CreatedAt: now}); err != nil {
		t.Fatalf("AddMemory(session): %v", err)
	}

	// Default (empty scope) must see only global atoms: session-scoped atoms
	// are working context and must not leak into cross-session recall.
	out, err := (&MemorySearchTool{DB: db}).Execute(context.Background(), `{"query":"fact"}`)
	if err != nil {
		t.Fatalf("Search(default): %v", err)
	}
	if !strings.Contains(out, "global fact") || strings.Contains(out, "session fact") {
		t.Fatalf("default search = %q, want global only", out)
	}

	// An explicit session-scoped tool sees only session atoms.
	out, err = (&MemorySearchTool{DB: db, Scope: memory.ScopeSession}).Execute(context.Background(), `{"query":"fact"}`)
	if err != nil {
		t.Fatalf("Search(session): %v", err)
	}
	if !strings.Contains(out, "session fact") || strings.Contains(out, "global fact") {
		t.Fatalf("session search = %q, want session only", out)
	}
}

func TestMemorySearchTool_RejectsUnknownScope(t *testing.T) {
	db := openScopeTestDB(t)
	tool := &MemorySearchTool{DB: db, Scope: "project"} // unknown tier
	if _, err := tool.Execute(context.Background(), `{"query":"x"}`); err == nil {
		t.Fatal("memory_search with an unknown scope should fail loudly")
	}
}
