package yaah

// memory_search_test.go pins the CLI's stale-width note (PR #824 review
// finding 1): `yaah memory search` is the inspection view, so a page that is
// missing rows the vector path could not compare has to say so on every path —
// the semantic page, the keyword fall-through, and the empty result. The note
// used to be printed only when the vector page had hits, which is exactly when
// the omission is least likely to matter.

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/buchenberg/yaah/internal/memory"
	"github.com/spf13/cobra"
)

// widthEmbedSearch stands in for an embedding model at a fixed width, so a test
// can stand on either side of a model swap: rows embedded at one width cannot
// be compared with a query embedded at another.
type widthEmbedSearch struct{ dims int }

func (e widthEmbedSearch) Embed(context.Context, string) (memory.Embedding, error) {
	v := make(memory.Embedding, e.dims)
	for i := range v {
		v[i] = 0.5 + float32(i)*0.01
	}
	return v, nil
}

// newMemorySearchCmd builds the flag surface searchMemoryCLI writes through,
// with its output captured in a buffer.
func newMemorySearchCmd(t *testing.T) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	cmd := &cobra.Command{Use: "search"}
	cmd.Flags().String("scope", "", "Tier filter: all (default), global, or session")
	cmd.SetContext(context.Background())
	buf := &bytes.Buffer{}
	cmd.SetOut(buf)
	return cmd, buf
}

// openCLISearchDB opens a throwaway store: the CLI's own DB path is a global,
// so the search is exercised through a DB the test owns.
func openCLISearchDB(t *testing.T) *memory.DB {
	t.Helper()
	db, err := memory.Open(filepath.Join(t.TempDir(), "cli-search.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// seedCLIEntry adds a row the keyword path can return.
func seedCLIEntry(t *testing.T, db *memory.DB, e memory.Entry) {
	t.Helper()
	if e.CreatedAt == 0 {
		e.CreatedAt = time.Now().Unix()
	}
	if err := db.AddMemory(e); err != nil {
		t.Fatalf("AddMemory(%s): %v", e.ID, err)
	}
}

// seedEmbeddedCLIEntry additionally waits for the background embed, so the row
// carries an embedding at the currently configured width.
func seedEmbeddedCLIEntry(t *testing.T, db *memory.DB, e memory.Entry) {
	t.Helper()
	seedCLIEntry(t, db, e)
	ch := db.EmbedMemoryAsync(e.ID, e.Text)
	if ch == nil {
		t.Fatalf("EmbedMemoryAsync(%s) returned nil with an embedder configured", e.ID)
	}
	<-ch
}

func TestSearchMemoryCLI_DimMismatchNoteEveryPath(t *testing.T) {
	// The ANSI wrappers honor NO_COLOR, so the assertions can look for plain
	// text.
	t.Setenv("NO_COLOR", "1")

	t.Run("keyword fall-through notes the omission", func(t *testing.T) {
		db := openCLISearchDB(t)
		db.SetEmbedder(widthEmbedSearch{dims: 3})
		seedEmbeddedCLIEntry(t, db, memory.Entry{ID: "stale", Text: "llama grooming schedule", Scope: memory.ScopeGlobal})
		// No embedding: only the keyword path can return it, so the vector page
		// is empty while FTS has a row — the fall-through branch.
		seedCLIEntry(t, db, memory.Entry{ID: "kw", Text: "kubernetes cluster tuning", Scope: memory.ScopeGlobal})
		db.SetEmbedder(widthEmbedSearch{dims: 5})

		cmd, buf := newMemorySearchCmd(t)
		if err := searchMemoryCLI(cmd, db, "kubernetes"); err != nil {
			t.Fatalf("searchMemoryCLI: %v", err)
		}
		out := buf.String()
		if !strings.Contains(out, "kw") {
			t.Fatalf("the keyword path lost its row: %q", out)
		}
		if !strings.Contains(out, "different embedding dimension") || !strings.Contains(out, "3: 1 row(s)") {
			t.Fatalf("the keyword fall-through dropped the dimension note: %q", out)
		}
	})

	t.Run("empty result notes the omission", func(t *testing.T) {
		db := openCLISearchDB(t)
		db.SetEmbedder(widthEmbedSearch{dims: 3})
		seedEmbeddedCLIEntry(t, db, memory.Entry{ID: "stale", Text: "llama grooming schedule", Scope: memory.ScopeGlobal})
		db.SetEmbedder(widthEmbedSearch{dims: 5})

		cmd, buf := newMemorySearchCmd(t)
		if err := searchMemoryCLI(cmd, db, "kubernetes"); err != nil {
			t.Fatalf("searchMemoryCLI: %v", err)
		}
		out := buf.String()
		if !strings.Contains(out, "No matching memories found.") {
			t.Fatalf("the unrelated stale row must not pad the page: %q", out)
		}
		if !strings.Contains(out, "different embedding dimension") || !strings.Contains(out, "3: 1 row(s)") {
			t.Fatalf("the empty-result path dropped the dimension note: %q", out)
		}
	})

	t.Run("semantic page keeps the note", func(t *testing.T) {
		db := openCLISearchDB(t)
		db.SetEmbedder(widthEmbedSearch{dims: 3})
		seedEmbeddedCLIEntry(t, db, memory.Entry{ID: "stale", Text: "postgres pgbouncer tuning", Scope: memory.ScopeGlobal})
		db.SetEmbedder(widthEmbedSearch{dims: 5})
		seedEmbeddedCLIEntry(t, db, memory.Entry{ID: "vec", Text: "postgres connection pooling", Scope: memory.ScopeGlobal})

		cmd, buf := newMemorySearchCmd(t)
		if err := searchMemoryCLI(cmd, db, "postgres"); err != nil {
			t.Fatalf("searchMemoryCLI: %v", err)
		}
		out := buf.String()
		if !strings.Contains(out, "vec") {
			t.Fatalf("the semantic page lost its row: %q", out)
		}
		if !strings.Contains(out, "different embedding dimension") {
			t.Fatalf("the semantic page dropped the dimension note: %q", out)
		}
	})

	t.Run("a healthy store prints no note", func(t *testing.T) {
		db := openCLISearchDB(t)
		db.SetEmbedder(widthEmbedSearch{dims: 5})
		seedEmbeddedCLIEntry(t, db, memory.Entry{ID: "vec", Text: "kubernetes cluster tuning", Scope: memory.ScopeGlobal})

		cmd, buf := newMemorySearchCmd(t)
		if err := searchMemoryCLI(cmd, db, "kubernetes"); err != nil {
			t.Fatalf("searchMemoryCLI: %v", err)
		}
		if out := buf.String(); strings.Contains(out, "different embedding dimension") {
			t.Fatalf("a store at the current width reported a mismatch: %q", out)
		}
	})
}
