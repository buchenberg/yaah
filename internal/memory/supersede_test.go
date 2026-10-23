package memory

import (
	"errors"
	"path/filepath"
	"testing"
)

func openSupersedeTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestSupersedeHidesOldAtom pins the property the whole mechanism exists for: a
// contradicted atom stops surfacing in every read path, while staying on the
// record. Silently retaining the old claim beside its correction is the failure
// mode being designed out.
func TestSupersedeHidesOldAtom(t *testing.T) {
	db := openSupersedeTestDB(t)
	if err := db.AddMemory(Entry{ID: "old", Text: "yaah returns authMethods: []", Scope: ScopeGlobal, CreatedAt: 1}); err != nil {
		t.Fatalf("AddMemory(old): %v", err)
	}
	if err := db.AddMemory(Entry{ID: "new", Text: "yaah returns authMethods: [auth_token, mcp_oauth]", Scope: ScopeGlobal, CreatedAt: 2}); err != nil {
		t.Fatalf("AddMemory(new): %v", err)
	}
	if err := db.SupersedeMemory("old", "new"); err != nil {
		t.Fatalf("SupersedeMemory: %v", err)
	}

	got, err := db.SearchMemoryScoped("authMethods", 10, "", ScopeAll)
	if err != nil {
		t.Fatalf("SearchMemoryScoped: %v", err)
	}
	for _, e := range got {
		if e.ID == "old" {
			t.Fatalf("superseded atom %q still surfaced in search", e.ID)
		}
	}
	if len(got) != 1 || got[0].ID != "new" {
		t.Fatalf("search returned %d results, want exactly the replacement", len(got))
	}

	if listed, err := db.ListMemoryScoped(10, ScopeAll); err != nil {
		t.Fatalf("ListMemoryScoped: %v", err)
	} else {
		for _, e := range listed {
			if e.ID == "old" {
				t.Fatal("superseded atom still listed")
			}
		}
	}

	if n, err := db.CountMemories(ScopeAll); err != nil {
		t.Fatalf("CountMemories: %v", err)
	} else if n != 1 {
		t.Fatalf("CountMemories = %d, want 1 live atom", n)
	}

	// The record survives: supersession marks, it does not delete.
	by, err := db.SupersededBy("old")
	if err != nil {
		t.Fatalf("SupersededBy: %v", err)
	}
	if by != "new" {
		t.Fatalf("SupersededBy(old) = %q, want \"new\"", by)
	}
	if _, err := db.GetMemory("old"); err != nil {
		t.Fatalf("superseded atom should still be retrievable by id: %v", err)
	}
}

// TestSupersedeRejectsBadTargets covers the three ways a supersede can be
// wrong: a missing target, a double supersede, and an atom superseding itself.
func TestSupersedeRejectsBadTargets(t *testing.T) {
	db := openSupersedeTestDB(t)
	for _, id := range []string{"a", "b", "c"} {
		if err := db.AddMemory(Entry{ID: id, Text: "fact " + id, Scope: ScopeGlobal, CreatedAt: 1}); err != nil {
			t.Fatalf("AddMemory(%s): %v", id, err)
		}
	}
	if err := db.SupersedeMemory("missing", "a"); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("supersede of a missing atom = %v, want ErrMemoryNotFound", err)
	}
	if err := db.SupersedeMemory("a", "a"); err == nil {
		t.Fatal("an atom superseded itself; that must be refused")
	}
	if err := db.SupersedeMemory("a", "b"); err != nil {
		t.Fatalf("SupersedeMemory(a,b): %v", err)
	}
	if err := db.SupersedeMemory("a", "c"); err == nil {
		t.Fatal("a second supersede of the same atom succeeded; provenance must not be silently re-pointed")
	}
}

// TestDedupeIgnoresSupersededAtoms pins that a correction can restate an atom
// that was itself superseded earlier: the dedupe lookup must not match dead
// rows, or a fact could never be re-asserted once it had been retired.
func TestDedupeIgnoresSupersededAtoms(t *testing.T) {
	db := openSupersedeTestDB(t)
	if err := db.AddMemory(Entry{ID: "old", Text: "the gate runs on linux", Scope: ScopeGlobal, CreatedAt: 1}); err != nil {
		t.Fatalf("AddMemory: %v", err)
	}
	if err := db.AddMemory(Entry{ID: "replacement", Text: "the gate runs on linux and windows", Scope: ScopeGlobal, CreatedAt: 2}); err != nil {
		t.Fatalf("AddMemory: %v", err)
	}
	if err := db.SupersedeMemory("old", "replacement"); err != nil {
		t.Fatalf("SupersedeMemory: %v", err)
	}
	id, err := db.AddMemoryDedup(Entry{ID: "again", Text: "the gate runs on linux", Scope: ScopeGlobal, CreatedAt: 2})
	if err != nil {
		t.Fatalf("AddMemoryDedup: %v", err)
	}
	if id == "old" {
		t.Fatal("dedupe matched a superseded atom and refused to re-assert the fact")
	}
}

// TestPromoteMemory covers promotion: a session atom becomes reachable by
// default recall, the prior tier is reported, and the move is idempotent.
func TestPromoteMemory(t *testing.T) {
	db := openSupersedeTestDB(t)
	if err := db.AddMemory(Entry{ID: "s1", Text: "the gate requires a beads prefix", Scope: ScopeSession, CreatedAt: 1}); err != nil {
		t.Fatalf("AddMemory: %v", err)
	}
	before, err := db.SearchMemoryScoped("beads prefix", 10, "", ScopeGlobal)
	if err != nil {
		t.Fatalf("SearchMemoryScoped: %v", err)
	}
	if len(before) != 0 {
		t.Fatalf("session atom was visible in global recall before promotion: %d hits", len(before))
	}

	from, err := db.PromoteMemory("s1")
	if err != nil {
		t.Fatalf("PromoteMemory: %v", err)
	}
	if from != ScopeSession {
		t.Fatalf("PromoteMemory reported prior tier %q, want %q", from, ScopeSession)
	}

	after, err := db.SearchMemoryScoped("beads prefix", 10, "", ScopeGlobal)
	if err != nil {
		t.Fatalf("SearchMemoryScoped: %v", err)
	}
	if len(after) != 1 || after[0].ID != "s1" {
		t.Fatalf("promoted atom not reachable by default recall: %+v", after)
	}

	// Idempotent: a retry after a partial failure must not fail.
	if from, err := db.PromoteMemory("s1"); err != nil || from != ScopeGlobal {
		t.Fatalf("PromoteMemory retry = (%q, %v), want (%q, nil)", from, err, ScopeGlobal)
	}
}

// TestPromoteMemoryRejectsBadTargets pins that promotion cannot reach across
// the supersede boundary or invent an atom.
func TestPromoteMemoryRejectsBadTargets(t *testing.T) {
	db := openSupersedeTestDB(t)
	if err := db.AddMemory(Entry{ID: "s1", Text: "session fact", Scope: ScopeSession, CreatedAt: 1}); err != nil {
		t.Fatalf("AddMemory: %v", err)
	}
	if err := db.AddMemory(Entry{ID: "s2", Text: "other session fact", Scope: ScopeSession, CreatedAt: 1}); err != nil {
		t.Fatalf("AddMemory: %v", err)
	}
	if err := db.SupersedeMemory("s1", "s2"); err != nil {
		t.Fatalf("SupersedeMemory: %v", err)
	}
	if _, err := db.PromoteMemory("missing"); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("promote of a missing atom = %v, want ErrMemoryNotFound", err)
	}
	if _, err := db.PromoteMemory("s1"); err == nil {
		t.Fatal("promoted a superseded atom; its replacement should be promoted instead")
	}
}

// TestSupersedeRefusesSessionOverGlobal pins the one-way tier rule: a session
// atom may not retire a global fact. The global claim would leave recall and
// then vanish with the session atom, which is a silent demotion of exactly the
// knowledge the tiers protect.
func TestSupersedeRefusesSessionOverGlobal(t *testing.T) {
	db := openSupersedeTestDB(t)
	if err := db.AddMemory(Entry{ID: "g", Text: "the gate runs on linux and windows", Scope: ScopeGlobal, CreatedAt: 1}); err != nil {
		t.Fatalf("AddMemory: %v", err)
	}
	if err := db.AddMemory(Entry{ID: "s", Text: "the gate now also runs on darwin", Scope: ScopeSession, CreatedAt: 2}); err != nil {
		t.Fatalf("AddMemory: %v", err)
	}
	if err := db.SupersedeMemory("g", "s"); err == nil {
		t.Fatal("a session atom superseded a global atom; that demotes knowledge silently")
	}
	if got, err := db.SupersededBy("g"); err != nil || got != "" {
		t.Fatalf("global atom was marked despite the refusal: (%q, %v)", got, err)
	}
}

// TestSupersedeRefusesDeadReplacement pins that a superseded atom cannot act as
// a replacement: chaining corrections onto a retired atom hides both.
func TestSupersedeRefusesDeadReplacement(t *testing.T) {
	db := openSupersedeTestDB(t)
	for _, id := range []string{"a", "b", "c"} {
		if err := db.AddMemory(Entry{ID: id, Text: "fact " + id, Scope: ScopeGlobal, CreatedAt: 1}); err != nil {
			t.Fatalf("AddMemory(%s): %v", id, err)
		}
	}
	// Retire b itself, then try to chain a correction onto it.
	if err := db.SupersedeMemory("b", "c"); err != nil {
		t.Fatalf("SupersedeMemory(b,c): %v", err)
	}
	if err := db.SupersedeMemory("a", "b"); err == nil {
		t.Fatal("a superseded atom was used as a replacement")
	}
}

// TestAddMemorySupersedingIsAtomic pins that a rejected supersede leaves no
// trace of the new atom: the correction and the retirement of the old claim
// happen together or not at all.
func TestAddMemorySupersedingIsAtomic(t *testing.T) {
	db := openSupersedeTestDB(t)
	if err := db.AddMemory(Entry{ID: "g", Text: "old claim about ports", Scope: ScopeGlobal, CreatedAt: 1}); err != nil {
		t.Fatalf("AddMemory: %v", err)
	}
	if _, _, err := db.AddMemorySuperseding(
		Entry{ID: "s", Text: "new claim about ports", Scope: ScopeSession, CreatedAt: 2}, "g",
	); err == nil {
		t.Fatal("session-over-global supersede was accepted through AddMemorySuperseding")
	}
	if n, err := db.CountMemories(ScopeAll); err != nil {
		t.Fatalf("CountMemories: %v", err)
	} else if n != 1 {
		t.Fatalf("store has %d atoms after the refused write, want 1", n)
	}
	if got, err := db.SupersededBy("g"); err != nil || got != "" {
		t.Fatalf("global atom was retired despite the refusal: (%q, %v)", got, err)
	}
}

// TestStatsExcludeSuperseded pins that superseded atoms leave the aggregate:
// "how much does this agent know" must not count retired claims.
func TestStatsExcludeSuperseded(t *testing.T) {
	db := openSupersedeTestDB(t)
	if err := db.AddMemory(Entry{ID: "old", Text: "retired fact", Scope: ScopeGlobal, CreatedAt: 1}); err != nil {
		t.Fatalf("AddMemory: %v", err)
	}
	if err := db.AddMemory(Entry{ID: "new", Text: "replacement fact", Scope: ScopeGlobal, CreatedAt: 2}); err != nil {
		t.Fatalf("AddMemory: %v", err)
	}
	if err := db.SupersedeMemory("old", "new"); err != nil {
		t.Fatalf("SupersedeMemory: %v", err)
	}
	st, err := db.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st.Total != 1 {
		t.Fatalf("Stats().Total = %d, want 1 live atom", st.Total)
	}
}
