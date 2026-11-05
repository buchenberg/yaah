package catalog

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
)

// The checked-in snapshot must decode cleanly: TestEmbeddedSnapshotInvariants
// plus the generator's determinism tests are what make a regeneration a
// reviewable diff instead of an unverifiable blob.
func TestEmbeddedSnapshotInvariants(t *testing.T) {
	snap, err := Parse(Embedded())
	if err != nil {
		t.Fatalf("Parse(Embedded()) error: %v", err)
	}

	t.Run("snapshot is not empty", func(t *testing.T) {
		if len(snap.Models) < 1000 {
			t.Errorf("snapshot carries %d models, want at least 1000", len(snap.Models))
		}
		if snap.Source != SourceURL {
			t.Errorf("Source = %q, want %q", snap.Source, SourceURL)
		}
	})

	t.Run("model keys are normalized", func(t *testing.T) {
		for key := range snap.Models {
			if key == "" {
				t.Error("empty model key")
				continue
			}
			if got := Normalize(key); got != key {
				t.Errorf("key %q is not normalized (Normalize gives %q)", key, got)
			}
			if key != strings.ToLower(key) {
				t.Errorf("key %q is not lowercase", key)
			}
		}
	})

	t.Run("aliases point at known, distinct targets", func(t *testing.T) {
		for src, dst := range snap.Aliases {
			if src == dst {
				t.Errorf("alias %q points at itself", src)
			}
			if _, ok := snap.Models[dst]; !ok {
				t.Errorf("alias %q targets unknown model %q", src, dst)
			}
			if _, chained := snap.Aliases[dst]; chained {
				t.Errorf("alias %q targets %q, which is itself an alias (chains must be flattened)", src, dst)
			}
		}
	})

	t.Run("lookup never panics on odd input", func(t *testing.T) {
		for _, name := range []string{"", "/", "///", "-", "provider/", "UPPER/Case"} {
			if _, ok := Lookup(name); ok {
				t.Errorf("Lookup(%q) reported a hit", name)
			}
		}
	})
}

// The generator writes Marshal's output, so re-encoding the checked-in bytes
// must reproduce them exactly. This pins the file to a canonical form: a
// hand edit that reorders keys or changes indentation fails here, and a
// regeneration is guaranteed to be a content diff rather than a formatting
// diff.
func TestEmbeddedSnapshotRoundTripsByteIdentical(t *testing.T) {
	data := Embedded()
	snap, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse(Embedded()) error: %v", err)
	}
	out, err := snap.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	if !bytes.Equal(out, data) {
		t.Fatalf("re-encoding the snapshot is not byte-identical (%d bytes in, %d bytes out); regenerate with go run ./tools/cataloggen", len(data), len(out))
	}
}

func TestParseRejectsBadSnapshots(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{"unknown field", `{"schema":1,"source":"x","models":{},"pricing":{}}`},
		{"schema mismatch", `{"schema":99,"source":"x","models":{}}`},
		{"trailing data", `{"schema":1,"source":"x","models":{}}{"schema":1}`},
		{"not json", `nope`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse([]byte(tt.data)); err == nil {
				t.Fatalf("Parse(%s) succeeded, want error", tt.data)
			}
		})
	}
}

// A broken embedded snapshot must not be silent: the loader degrades to an
// empty catalog (no panics, no nil index) AND reports why, so a bad build shows
// up as a warning and a DecodeErr instead of as a catalog that mysteriously
// knows nothing.
func TestBuildIndexReportsCorruptSnapshot(t *testing.T) {
	ix, err := buildIndex([]byte(`{"schema":1,"models":`))
	if err == nil {
		t.Fatal("buildIndex(corrupt) succeeded, want a decode error")
	}
	if ix != nil {
		t.Errorf("buildIndex(corrupt) returned an index alongside its error: %v", ix)
	}

	// The degradation policy: an empty, non-nil index that answers "unknown".
	degraded, err := indexFromData([]byte(`nope`))
	if err == nil {
		t.Fatal("indexFromData(corrupt) swallowed the decode error")
	}
	if degraded == nil {
		t.Fatal("indexFromData(corrupt) returned a nil index; every lookup would panic")
	}
	if len(degraded.models) != 0 {
		t.Errorf("degraded index carries %d models, want 0", len(degraded.models))
	}
	if e, ok := degraded.lookup("gpt-4o"); ok {
		t.Errorf("degraded index claimed to know gpt-4o: %+v", e)
	}
}

// The shipped snapshot must decode, so DecodeErr is nil and Lookup works. This
// also forces the one-time load, proving the fetch is wired rather than
// accidentally shadowed by a test seam.
func TestDecodeErrNilForEmbeddedSnapshot(t *testing.T) {
	if err := DecodeErr(); err != nil {
		t.Fatalf("DecodeErr() = %v, want nil for the checked-in snapshot", err)
	}
	if Len() < 1000 {
		t.Errorf("Len() = %d after a clean load, want at least 1000", Len())
	}
}

// withStubbedDecode swaps the once-loader and its decode seam for the duration
// of one test, so the failure branch — unreachable from a checked-in snapshot —
// can be exercised. Everything it touches is restored on cleanup.
func withStubbedDecode(t *testing.T, decode func() (*index, error)) {
	t.Helper()
	prevLoaded, prevDecode, prevErr := loaded, decodeSnapshot, decodeErr
	decodeSnapshot = decode
	loaded = sync.OnceValue(loadIndex)
	decodeErr = nil
	t.Cleanup(func() {
		loaded, decodeSnapshot, decodeErr = prevLoaded, prevDecode, prevErr
	})
}

// A snapshot that fails to decode must surface through DecodeErr, not only
// through a log line: DecodeErr is what session startup calls before a surface
// can hide stderr (cmd/yaah's loadProviderCatalog).
func TestDecodeErrSurfacesDecodeFailure(t *testing.T) {
	sentinel := errors.New("catalog: decode snapshot: unexpected EOF")
	withStubbedDecode(t, func() (*index, error) { return emptyIndex(), sentinel })

	if err := DecodeErr(); !errors.Is(err, sentinel) {
		t.Fatalf("DecodeErr() = %v, want the decode error %v", err, sentinel)
	}
	// The no-panic degradation still holds: an empty, usable index.
	if Len() != 0 {
		t.Errorf("Len() = %d after a failed load, want 0", Len())
	}
	if _, ok := Lookup("gpt-4o"); ok {
		t.Error("a failed load still claimed to know gpt-4o")
	}
}

// The load is a Once: reporting the error must not re-run a failed decode, or a
// warning would appear per lookup.
func TestDecodeRunsOnceAcrossCallers(t *testing.T) {
	calls := 0
	withStubbedDecode(t, func() (*index, error) {
		calls++
		return emptyIndex(), errors.New("boom")
	})
	_ = DecodeErr()
	_ = DecodeErr()
	_ = Len()
	if calls != 1 {
		t.Errorf("decode ran %d times, want 1 — the snapshot is decoded once per process", calls)
	}
}

// A seam that returns no index at all must still degrade: a nil index would
// panic the first lookup.
func TestDecodeNilIndexDegrades(t *testing.T) {
	withStubbedDecode(t, func() (*index, error) { return nil, errors.New("boom") })
	if err := DecodeErr(); err == nil {
		t.Fatal("DecodeErr() = nil, want the decode error")
	}
	if e, ok := Lookup("gpt-4o"); ok {
		t.Errorf("Lookup on a degraded catalog reported a hit: %+v", e)
	}
}

// The "never nil" guarantee has to hold for every seam, not just the error
// branch: a seam that reports no error and still hands back no index must
// degrade too, or the next lookup dereferences nil.
func TestDecodeNilIndexWithoutErrorDegrades(t *testing.T) {
	withStubbedDecode(t, func() (*index, error) { return nil, nil })
	if err := DecodeErr(); err != nil {
		t.Fatalf("DecodeErr() = %v, want nil for a seam that reports no error", err)
	}
	if Len() != 0 {
		t.Errorf("Len() = %d for a nil-yielding seam, want 0", Len())
	}
	if e, ok := Lookup("gpt-4o"); ok {
		t.Errorf("Lookup on a nil-yielding catalog reported a hit: %+v", e)
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"gpt-4o", "gpt-4o"},
		{"google/gemini-2.5-pro", "gemini-2.5-pro"},
		{"models/google/gemini-2.5-pro", "gemini-2.5-pro"},
		{"OpenAI/GPT-4o", "gpt-4o"},
		{"gpt-4.1", "gpt-4.1"}, // dots survive: "gpt-4.1" must not key "gpt-4"
		{"", ""},
		{"provider/", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Normalize(tt.name); got != tt.want {
				t.Errorf("Normalize(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestHyphenPrefixes(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"gpt-4o", []string{"gpt-4o", "gpt"}},
		{"gpt-4.1", []string{"gpt-4.1", "gpt"}},
		{"claude-sonnet-4-5-20250929", []string{"claude-sonnet-4-5-20250929", "claude-sonnet-4-5", "claude-sonnet-4", "claude-sonnet", "claude"}},
		{"plain", []string{"plain"}},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got := hyphenPrefixes(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("hyphenPrefixes(%q) = %v, want %v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("hyphenPrefixes(%q) = %v, want %v", tt.in, got, tt.want)
				}
			}
		})
	}
}

// testIndex exercises the resolution order without depending on what the
// pinned snapshot happens to contain.
func testIndex() *index {
	return newIndex(&Snapshot{
		Schema: schemaVersion,
		Source: "test",
		Models: map[string]Entry{
			"gpt-4o":            {Context: 128000, Output: 16384, ToolCall: true, Family: "gpt"},
			"gpt-4.1":           {Context: 1047576, Output: 32768, ToolCall: true, Family: "gpt"},
			"claude-sonnet-4-5": {Context: 200000, Output: 64000, Reasoning: true, Family: "claude-sonnet"},
			"claude-sonnet-3-7": {Context: 200000, Output: 8192, Family: "claude-sonnet"},
			"mystery-model":     {Context: 8192},
		},
		Aliases: map[string]string{"gpt-4o-latest": "gpt-4o"},
	})
}

func TestIndexLookup(t *testing.T) {
	ix := testIndex()
	tests := []struct {
		name      string
		model     string
		want      int
		wantFound bool
	}{
		{"exact", "gpt-4o", 128000, true},
		{"provider prefix stripped", "openai/gpt-4o", 128000, true},
		{"nested provider prefix stripped", "models/openai/gpt-4o", 128000, true},
		{"case insensitive", "OpenAI/GPT-4O", 128000, true},
		{"dated variant resolves by prefix", "gpt-4o-2024-08-06", 128000, true},
		{"longest prefix wins over family", "claude-sonnet-4-5-20250929", 200000, true},
		{"dot is not a prefix boundary", "gpt-4.1", 1047576, true},
		{"alias resolves", "gpt-4o-latest", 128000, true},
		{"unknown", "totally-unknown-42", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, ok := ix.lookup(tt.model)
			if ok != tt.wantFound {
				t.Fatalf("lookup(%q) found = %v, want %v", tt.model, ok, tt.wantFound)
			}
			if e.Context != tt.want {
				t.Errorf("lookup(%q).Context = %d, want %d", tt.model, e.Context, tt.want)
			}
		})
	}
}

func TestIndexFamilyFallback(t *testing.T) {
	ix := testIndex()

	t.Run("family name prefixes an unknown model", func(t *testing.T) {
		e, ok := ix.lookup("claude-sonnet-9")
		if !ok {
			t.Fatal("lookup(claude-sonnet-9) found nothing, want the claude-sonnet family")
		}
		// The family representative is conservative: the smallest non-zero
		// limits across family members, reasoning OR-ed.
		if e.Context != 200000 {
			t.Errorf("family Context = %d, want 200000", e.Context)
		}
		if e.Output != 8192 {
			t.Errorf("family Output = %d, want 8192 (smallest non-zero member)", e.Output)
		}
		if !e.Reasoning {
			t.Error("family Reasoning = false, want true (any member reasons)")
		}
	})

	t.Run("model IDs win over family names", func(t *testing.T) {
		e, _ := ix.lookup("claude-sonnet-4-5")
		if e.Output != 64000 {
			t.Errorf("Output = %d, want 64000 from the model entry, not the family", e.Output)
		}
	})

	t.Run("a bare family hit on an unrelated ID", func(t *testing.T) {
		e, ok := ix.lookup("gpt-45")
		if !ok {
			t.Fatal("lookup(gpt-45) found nothing, want the gpt family")
		}
		if e.Context != 128000 {
			t.Errorf("Context = %d, want 128000 (smallest gpt member)", e.Context)
		}
	})
}

func TestAccessors(t *testing.T) {
	// These use the embedded snapshot. They assert the shape and semantics of
	// an answer rather than exact numbers, so a regeneration rarely breaks
	// them while a broken layer still fails loudly.
	t.Run("known model with a provider prefix", func(t *testing.T) {
		w, ok := ContextWindow("openai/gpt-4o")
		if !ok || w <= 0 {
			t.Fatalf("ContextWindow(openai/gpt-4o) = %d, %v; want a positive window", w, ok)
		}
		if limit, ok := OutputLimit("gpt-4o"); !ok || limit <= 0 {
			t.Errorf("OutputLimit(gpt-4o) = %d, %v; want a positive limit", limit, ok)
		}
	})

	t.Run("reasoning distinguishes unknown from non-reasoning", func(t *testing.T) {
		if reasoning, ok := Reasoning("gpt-4o"); !ok || reasoning {
			t.Errorf("Reasoning(gpt-4o) = %v, %v; want known non-reasoning (false, true)", reasoning, ok)
		}
		if _, ok := Reasoning("definitely-not-a-model-42"); ok {
			t.Error("Reasoning(unknown) reported known, want unknown")
		}
	})

	t.Run("a dot never borrows a neighbouring family", func(t *testing.T) {
		dot, ok := ContextWindow("gpt-4.1")
		if !ok {
			t.Fatal("ContextWindow(gpt-4.1) has no answer")
		}
		hyphen, _ := ContextWindow("gpt-4")
		if dot == hyphen {
			t.Errorf("gpt-4.1 and gpt-4 resolve to the same window %d; dots must not be prefix boundaries", dot)
		}
	})
}

func TestParseShape(t *testing.T) {
	// Parse must accept the generator's exact output shape, including the
	// tool_call field and an optional alias table.
	data := []byte(`{
	  "schema": 1,
	  "source": "https://models.dev/api.json",
	  "models": {
	    "a": {
	      "context": 1,
	      "output": 2,
	      "reasoning": true,
	      "tool_call": true,
	      "family": "fam"
	    }
	  },
	  "aliases": {
	    "b": "a"
	  }
	}`)
	snap, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	got := snap.Models["a"]
	want := Entry{Context: 1, Output: 2, Reasoning: true, ToolCall: true, Family: "fam"}
	if got != want {
		t.Errorf("entry = %+v, want %+v", got, want)
	}
	if snap.Aliases["b"] != "a" {
		t.Errorf("alias b = %q, want \"a\"", snap.Aliases["b"])
	}
}
