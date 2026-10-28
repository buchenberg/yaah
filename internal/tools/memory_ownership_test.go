package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/memory"
)

// memoryOwnershipCase is one (atom tier × atom owner × writer) combination
// probed through both of the tools that mutate an existing atom.
type memoryOwnershipCase struct {
	name string
	// The atom under test: its tier and the session that distilled it. An
	// empty session is an unlinked atom — the registry's or the CLI's own
	// write, which belongs to nobody in particular.
	entryScope   string
	entrySession string
	// The writer: what the tool instance is configured to allow.
	writerScope   string
	writerSession string
	// wantUpdateRefused / wantPromoteRefused say whether the call must fail.
	wantUpdateRefused  bool
	wantPromoteRefused bool
}

// TestMemoryOwnershipGuard_UpdateAgreesWithPromote is the table the bead is
// about: the ownership guard must not be weaker on the write path than on the
// promotion path.
//
// The regression it pins: memory_update used to check ownership only inside
// `if t.Scope != ""`, so a writer with a session ID and no scope could rewrite
// any session's atom, while memory_promote refused the same atom. Every
// foreign-session row therefore demands the same answer from both tools —
// including the rows where the writer's scope is empty, which is exactly where
// the write path used to wave the call through.
func TestMemoryOwnershipGuard_UpdateAgreesWithPromote(t *testing.T) {
	cases := []memoryOwnershipCase{
		{
			name:       "owned session atom, owning writer",
			entryScope: memory.ScopeSession, entrySession: "sess-a",
			writerScope: memory.ScopeSession, writerSession: "sess-a",
		},
		{
			name:       "unowned session atom, session-scoped writer",
			entryScope: memory.ScopeSession, entrySession: "",
			writerScope: memory.ScopeSession, writerSession: "sess-a",
		},
		{
			name:       "foreign session atom, same scope and session mismatch",
			entryScope: memory.ScopeSession, entrySession: "sess-b",
			writerScope: memory.ScopeSession, writerSession: "sess-a",
			wantUpdateRefused: true, wantPromoteRefused: true,
		},
		{
			name:       "foreign session atom, writer has session but no scope",
			entryScope: memory.ScopeSession, entrySession: "sess-b",
			// The regression row: no scope configured means the old write path
			// skipped its guard entirely while promotion still refused.
			writerScope: "", writerSession: "sess-a",
			wantUpdateRefused: true, wantPromoteRefused: true,
		},
		{
			name:       "foreign global atom, writer has session but no scope",
			entryScope: memory.ScopeGlobal, entrySession: "sess-b",
			writerScope: "", writerSession: "sess-a",
			wantUpdateRefused: true, wantPromoteRefused: true,
		},
		{
			name:       "own global atom, session-scoped writer",
			entryScope: memory.ScopeGlobal, entrySession: "sess-a",
			writerScope: memory.ScopeSession, writerSession: "sess-a",
			// The tier guard fires on the write path (a session-tier writer may
			// not rewrite a global fact); promotion of an already-global atom
			// is a no-op message, not a refusal.
			wantUpdateRefused: true, wantPromoteRefused: false,
		},
		{
			name:       "unowned global atom, session-scoped writer",
			entryScope: memory.ScopeGlobal, entrySession: "",
			writerScope: memory.ScopeSession, writerSession: "sess-a",
			wantUpdateRefused: true, wantPromoteRefused: false,
		},
		{
			name:       "own global atom, unscoped writer",
			entryScope: memory.ScopeGlobal, entrySession: "sess-a",
			writerScope: "", writerSession: "sess-a",
		},
		{
			name:       "global atom, no writer restriction",
			entryScope: memory.ScopeGlobal, entrySession: "sess-b",
			writerScope: "", writerSession: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			db := openScopeTestDB(t)

			// Two atoms per case: promotion flips the tier, so the update probe
			// gets its own row and its assertions see the tier the case names.
			addAtom := func(text string) string {
				t.Helper()
				out, err := (&MemoryAddTool{DB: db, SessionID: tc.entrySession}).Execute(ctx,
					`{"text":"`+text+`","scope":"`+tc.entryScope+`"}`)
				if err != nil {
					t.Fatalf("memory_add(%s): %v", text, err)
				}
				return savedID(t, out)
			}
			updateID := addAtom("update probe for " + tc.name)
			promoteID := addAtom("promote probe for " + tc.name)

			update := &MemoryUpdateTool{DB: db, Scope: tc.writerScope, SessionID: tc.writerSession}
			_, updateErr := update.Execute(ctx, `{"id":"`+updateID+`","text":"rewritten"}`)
			if refused := updateErr != nil; refused != tc.wantUpdateRefused {
				t.Errorf("memory_update refused = %v (err %v), want refused = %v",
					refused, updateErr, tc.wantUpdateRefused)
			}
			if tc.wantUpdateRefused && updateErr != nil &&
				!strings.Contains(updateErr.Error(), "belongs to session") &&
				!strings.Contains(updateErr.Error(), "has scope") {
				t.Errorf("refusal %q does not say which guard fired", updateErr)
			}

			// A refusal must leave the atom as it was: a guard that reports
			// failure after writing would be the very dishonesty this epic is
			// about.
			after, err := db.GetMemory(updateID)
			if err != nil {
				t.Fatalf("GetMemory: %v", err)
			}
			if tc.wantUpdateRefused {
				if after.Text != "update probe for "+tc.name {
					t.Errorf("refused update still rewrote the atom: %q", after.Text)
				}
			} else if after.Text != "rewritten" {
				t.Errorf("permitted update did not take effect: %q", after.Text)
			}

			promote := &MemoryPromoteTool{DB: db, SessionID: tc.writerSession}
			if _, promoteErr := promote.Execute(ctx, `{"id":"`+promoteID+`"}`); (promoteErr != nil) != tc.wantPromoteRefused {
				t.Errorf("memory_promote refused = %v (err %v), want refused = %v",
					promoteErr != nil, promoteErr, tc.wantPromoteRefused)
			}
		})
	}
}

// TestMemoryOwnershipGuard_SessionOnlyWriterStillGuarded is the narrowest
// statement of the fix, kept separate so a future refactor cannot quietly
// re-nest the session check under the scope check: with no scope configured at
// all, a writer that names a session must still refuse a foreign atom — and
// update and promote must answer identically.
func TestMemoryOwnershipGuard_SessionOnlyWriterStillGuarded(t *testing.T) {
	ctx := context.Background()
	db := openScopeTestDB(t)
	out, err := (&MemoryAddTool{DB: db, SessionID: "sess-b"}).Execute(ctx,
		`{"text":"session b learned the rollout needs a canary","scope":"session"}`)
	if err != nil {
		t.Fatalf("memory_add: %v", err)
	}
	id := savedID(t, out)

	_, updateErr := (&MemoryUpdateTool{DB: db, SessionID: "sess-a"}).Execute(ctx,
		`{"id":"`+id+`","text":"session a rewrote it"}`)
	_, promoteErr := (&MemoryPromoteTool{DB: db, SessionID: "sess-a"}).Execute(ctx, `{"id":"`+id+`"}`)

	if updateErr == nil {
		t.Error("memory_update with a session and no scope rewrote another session's atom")
	}
	if promoteErr == nil {
		t.Error("memory_promote promoted another session's atom")
	}
	if (updateErr == nil) != (promoteErr == nil) {
		t.Errorf("the two paths disagree about the same atom: update %v, promote %v", updateErr, promoteErr)
	}

	// And the legitimate case still works: the owning session may rewrite its
	// own atom without naming a scope.
	if _, err := (&MemoryUpdateTool{DB: db, SessionID: "sess-b"}).Execute(ctx,
		`{"id":"`+id+`","text":"session b corrected itself"}`); err != nil {
		t.Fatalf("owning session could not update its own atom: %v", err)
	}
}
