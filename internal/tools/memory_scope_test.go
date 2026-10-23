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
			entries, err := db.ListMemoryScoped(10, memory.ScopeAll)
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

// TestMemoryAddTool_PersistsSessionID pins the session link a consolidator's
// writes carry (wired from the sub-agent runner): the session-close pruning
// pass finds a session's atoms only through it, so an empty instance default
// must stay empty rather than become a bogus link.
func TestMemoryAddTool_PersistsSessionID(t *testing.T) {
	cases := []struct {
		name      string
		sessionID string
		want      string
	}{
		{"session link recorded", "sess-42", "sess-42"},
		{"empty default stays empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openScopeTestDB(t)
			tool := &MemoryAddTool{DB: db, Scope: memory.ScopeSession, SessionID: tc.sessionID}
			if _, err := tool.Execute(context.Background(), `{"text":"session-linked atom"}`); err != nil {
				t.Fatalf("Execute: %v", err)
			}
			entries, err := db.ListMemoryScoped(10, memory.ScopeAll)
			if err != nil {
				t.Fatalf("ListMemoryScoped: %v", err)
			}
			if len(entries) != 1 {
				t.Fatalf("entries = %d, want 1", len(entries))
			}
			if entries[0].SessionID != tc.want {
				t.Errorf("SessionID = %q, want %q", entries[0].SessionID, tc.want)
			}
		})
	}
}

// TestMemorySearchTool_CallScopeArg pins the call-argument path — the one that
// makes atoms distilled in an earlier conversation reachable. The instance
// default stays global (session atoms must not surface in an unwidened recall);
// only an explicit "scope" in the call opens the session tier.
func TestMemorySearchTool_CallScopeArg(t *testing.T) {
	cases := []struct {
		name      string
		toolScope string
		args      string
		want      []string
		notWant   []string
	}{
		{"no arg stays global", "", `{"query":"fact"}`, []string{"global fact"}, []string{"session fact"}},
		{"arg reaches session tier", "", `{"query":"fact","scope":"session"}`, []string{"session fact"}, []string{"global fact"}},
		{"arg spans both tiers", "", `{"query":"fact","scope":"all"}`, []string{"global fact", "session fact"}, nil},
		{"arg outranks instance default", memory.ScopeSession, `{"query":"fact","scope":"global"}`, []string{"global fact"}, []string{"session fact"}},
		{"arg tolerates case and padding", "", `{"query":"fact","scope":" ALL "}`, []string{"global fact", "session fact"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openScopeTestDB(t)
			now := time.Now().Unix()
			for _, e := range []memory.Entry{
				{ID: "g1", Text: "global fact", Scope: memory.ScopeGlobal, CreatedAt: now},
				{ID: "s1", Text: "session fact", Scope: memory.ScopeSession, CreatedAt: now},
			} {
				if err := db.AddMemory(e); err != nil {
					t.Fatalf("AddMemory(%s): %v", e.ID, err)
				}
			}
			out, err := (&MemorySearchTool{DB: db, Scope: tc.toolScope}).Execute(context.Background(), tc.args)
			if err != nil {
				t.Fatalf("memory_search %s: %v", tc.args, err)
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Fatalf("memory_search(%s) = %q, want it to contain %q", tc.args, out, want)
				}
			}
			for _, absent := range tc.notWant {
				if strings.Contains(out, absent) {
					t.Fatalf("memory_search(%s) = %q, want no %q", tc.args, out, absent)
				}
			}
		})
	}
}

// TestMemorySearchTool_RejectsUnknownScopeArg pins that a bad argument fails as
// loudly as a bad instance default rather than silently falling back to a
// wider tier.
func TestMemorySearchTool_RejectsUnknownScopeArg(t *testing.T) {
	db := openScopeTestDB(t)
	if _, err := (&MemorySearchTool{DB: db}).Execute(context.Background(), `{"query":"x","scope":"everything"}`); err == nil {
		t.Fatal("memory_search with an unknown scope argument should fail loudly")
	}
}

// TestMemorySearchTool_ScopeSchema pins that the tool advertises the tiers it
// accepts: a schema without a "scope" property is what made session atoms
// write-only in the first place.
func TestMemorySearchTool_ScopeSchema(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Type string   `json:"type"`
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal((&MemorySearchTool{}).Schema(), &schema); err != nil {
		t.Fatalf("Schema is not valid JSON: %v", err)
	}
	scope, ok := schema.Properties["scope"]
	if !ok {
		t.Fatal("memory_search schema has no scope property")
	}
	if scope.Type != "string" {
		t.Fatalf("scope type = %q, want string", scope.Type)
	}
	got := map[string]bool{}
	for _, v := range scope.Enum {
		got[v] = true
	}
	for _, want := range []string{memory.ScopeGlobal, memory.ScopeSession, memory.ScopeAll} {
		if !got[want] {
			t.Errorf("scope enum %v missing %q", scope.Enum, want)
		}
	}
}
