package tools

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

var savedIDPattern = regexp.MustCompile(`\(id: ([^)]+)\)`)

func savedID(t *testing.T, out string) string {
	t.Helper()
	m := savedIDPattern.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no memory id in tool output: %q", out)
	}
	return m[1]
}

// TestMemoryAddTool_Supersedes covers the correction path end to end: the new
// atom is saved, the old one is marked, and search stops returning the claim
// that was corrected.
func TestMemoryAddTool_Supersedes(t *testing.T) {
	db := openScopeTestDB(t)
	add := &MemoryAddTool{DB: db}

	out, err := add.Execute(context.Background(), `{"text":"yaah returns authMethods: []","tags":"[\"project:yaah\"]"}`)
	if err != nil {
		t.Fatalf("memory_add (old): %v", err)
	}
	oldID := savedID(t, out)

	out, err = add.Execute(context.Background(), `{"text":"yaah returns authMethods: [auth_token, mcp_oauth]","supersedes":"`+oldID+`"}`)
	if err != nil {
		t.Fatalf("memory_add (correction): %v", err)
	}
	if !strings.Contains(out, "supersedes "+oldID) {
		t.Fatalf("memory_add did not report the supersede: %q", out)
	}
	newID := savedID(t, out)

	search := &MemorySearchTool{DB: db}
	got, err := search.Execute(context.Background(), `{"query":"authMethods","scope":"all"}`)
	if err != nil {
		t.Fatalf("memory_search: %v", err)
	}
	if strings.Contains(got, "authMethods: []") {
		t.Fatalf("corrected claim still surfaced: %q", got)
	}
	if !strings.Contains(got, newID) {
		t.Fatalf("replacement not surfaced: %q", got)
	}

	by, err := db.SupersededBy(oldID)
	if err != nil {
		t.Fatalf("SupersededBy: %v", err)
	}
	if by != newID {
		t.Fatalf("SupersededBy(%s) = %q, want %q", oldID, by, newID)
	}
}

// TestMemoryAddTool_RejectsUnknownSupersedeTarget pins that a correction aimed
// at an atom that does not exist fails before anything is written: a saved
// correction that corrects nothing is worse than a refused call.
func TestMemoryAddTool_RejectsUnknownSupersedeTarget(t *testing.T) {
	db := openScopeTestDB(t)
	add := &MemoryAddTool{DB: db}
	if _, err := add.Execute(context.Background(), `{"text":"orphan correction","supersedes":"does-not-exist"}`); err == nil {
		t.Fatal("memory_add accepted a supersedes target that does not exist")
	}
	n, err := db.CountMemories("all")
	if err != nil {
		t.Fatalf("CountMemories: %v", err)
	}
	if n != 0 {
		t.Fatalf("store gained %d atom(s) despite the rejected supersede", n)
	}
}

// TestMemoryPromoteTool covers promotion from the tool surface: a session atom
// becomes reachable by default recall, and promoting twice is a no-op message
// rather than an error.
func TestMemoryPromoteTool(t *testing.T) {
	db := openScopeTestDB(t)
	add := &MemoryAddTool{DB: db}
	out, err := add.Execute(context.Background(), `{"text":"the gate requires a beads prefix","scope":"session"}`)
	if err != nil {
		t.Fatalf("memory_add: %v", err)
	}
	id := savedID(t, out)

	search := &MemorySearchTool{DB: db}
	if got, err := search.Execute(context.Background(), `{"query":"beads prefix"}`); err != nil {
		t.Fatalf("memory_search: %v", err)
	} else if strings.Contains(got, id) {
		t.Fatalf("session atom visible in default recall before promotion: %q", got)
	}

	promote := &MemoryPromoteTool{DB: db}
	msg, err := promote.Execute(context.Background(), `{"id":"`+id+`"}`)
	if err != nil {
		t.Fatalf("memory_promote: %v", err)
	}
	if !strings.Contains(msg, "global") {
		t.Fatalf("memory_promote did not report the new tier: %q", msg)
	}

	if got, err := search.Execute(context.Background(), `{"query":"beads prefix"}`); err != nil {
		t.Fatalf("memory_search: %v", err)
	} else if !strings.Contains(got, id) {
		t.Fatalf("promoted atom not reachable by default recall: %q", got)
	}

	if msg, err := promote.Execute(context.Background(), `{"id":"`+id+`"}`); err != nil {
		t.Fatalf("memory_promote retry: %v", err)
	} else if !strings.Contains(msg, "already") {
		t.Fatalf("second promote = %q, want an 'already global' message", msg)
	}
}

// TestMemoryPromoteTool_RequiresID pins the argument contract.
func TestMemoryPromoteTool_RequiresID(t *testing.T) {
	db := openScopeTestDB(t)
	promote := &MemoryPromoteTool{DB: db}
	if _, err := promote.Execute(context.Background(), `{}`); err == nil {
		t.Fatal("memory_promote accepted a call with no id")
	}
	if _, err := promote.Execute(context.Background(), `{"id":"nope"}`); err == nil {
		t.Fatal("memory_promote accepted an id that does not exist")
	}
}

// TestMemoryPromoteTool_RefusesOtherSessionsAtoms pins the ownership guard: a
// session-scoped caller may promote what its own conversation distilled, not
// another session's working context. An unscoped orchestrator is unaffected.
func TestMemoryPromoteTool_RefusesOtherSessionsAtoms(t *testing.T) {
	db := openScopeTestDB(t)
	out, err := (&MemoryAddTool{DB: db, SessionID: "sess-a"}).Execute(
		context.Background(), `{"text":"session a learned the deploy needs a prefix","scope":"session"}`)
	if err != nil {
		t.Fatalf("memory_add: %v", err)
	}
	id := savedID(t, out)

	if _, err := (&MemoryPromoteTool{DB: db, SessionID: "sess-b"}).Execute(
		context.Background(), `{"id":"`+id+`"}`); err == nil {
		t.Fatal("a sub-agent promoted another session's atom to global")
	}
	if _, err := (&MemoryPromoteTool{DB: db, SessionID: "sess-a"}).Execute(
		context.Background(), `{"id":"`+id+`"}`); err != nil {
		t.Fatalf("owning session could not promote its own atom: %v", err)
	}
}

// TestMemoryAddTool_SupersedeIsAtomic pins that a refused supersede does not
// leave the correction behind: the write and the retirement of the old claim
// stand or fall together.
func TestMemoryAddTool_SupersedeIsAtomic(t *testing.T) {
	db := openScopeTestDB(t)
	add := &MemoryAddTool{DB: db}
	out, err := add.Execute(context.Background(), `{"text":"yaah ships linux and windows"}`)
	if err != nil {
		t.Fatalf("memory_add: %v", err)
	}
	globalID := savedID(t, out)

	// A session atom may not retire a global fact, so this must fail outright.
	if _, err := add.Execute(context.Background(),
		`{"text":"yaah ships linux, windows and darwin","scope":"session","supersedes":"`+globalID+`"}`); err == nil {
		t.Fatal("session atom superseded a global atom through memory_add")
	}
	n, err := db.CountMemories("all")
	if err != nil {
		t.Fatalf("CountMemories: %v", err)
	}
	if n != 1 {
		t.Fatalf("store has %d atoms after the refused correction, want 1", n)
	}
	if got, err := db.SupersededBy(globalID); err != nil || got != "" {
		t.Fatalf("global atom retired despite the refusal: (%q, %v)", got, err)
	}
}
