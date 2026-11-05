package main

import (
	"bytes"
	"testing"

	"github.com/buchenberg/yaah/internal/providers/catalog"
)

// syntheticUpstream is a miniature models.dev payload. It pins the generator's
// policy: first-party registrations beat reseller mirrors, non-chat artifacts
// and metadata-free rows are dropped, and an alias is emitted only when
// following it yields more metadata.
const syntheticUpstream = `{
  "acme": {
    "id": "acme",
    "name": "Acme",
    "models": {
      "gpt-4o": {
        "id": "gpt-4o",
        "family": "gpt",
        "reasoning": false,
        "tool_call": true,
        "limit": {"context": 128000, "output": 16384}
      },
      "alias-row": {
        "id": "alias-row",
        "canonical_model_id": "acme/gpt-4o",
        "limit": {"context": 128000, "output": 8192}
      },
      "empty-row": {"id": "empty-row"},
      "image-model": {
        "id": "image-model",
        "type": "image",
        "limit": {"context": 0, "output": 0}
      },
      "acme-reasoner": {
        "id": "acme-reasoner",
        "family": "acme-reasoning",
        "reasoning": true,
        "limit": {"context": 200000, "output": 64000}
      }
    }
  },
  "reseller": {
    "id": "reseller",
    "name": "Reseller",
    "models": {
      "gpt-4o": {
        "id": "gpt-4o",
        "canonical_model_id": "acme/gpt-4o",
        "tool_call": true,
        "limit": {"context": 999999, "output": 999999}
      },
      "reseller-only": {
        "id": "reseller-only",
        "canonical_model_id": "acme/gpt-4o",
        "limit": {"context": 128000, "output": 16384}
      }
    }
  }
}`

// wantSynthetic is the exact canonical output Build must produce for
// syntheticUpstream.
const wantSynthetic = `{
  "schema": 1,
  "source": "https://models.dev/api.json",
  "models": {
    "acme-reasoner": {
      "context": 200000,
      "output": 64000,
      "reasoning": true,
      "family": "acme-reasoning"
    },
    "alias-row": {
      "context": 128000,
      "output": 8192
    },
    "gpt-4o": {
      "context": 128000,
      "output": 16384,
      "tool_call": true,
      "family": "gpt"
    },
    "reseller-only": {
      "context": 128000,
      "output": 16384
    }
  },
  "aliases": {
    "alias-row": "gpt-4o"
  }
}
`

func TestBuildGolden(t *testing.T) {
	snap, err := Build([]byte(syntheticUpstream))
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	got, err := snap.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	if !bytes.Equal(got, []byte(wantSynthetic)) {
		t.Fatalf("Build output mismatch\n--- got ---\n%s\n--- want ---\n%s", got, wantSynthetic)
	}
}

func TestBuildPolicy(t *testing.T) {
	snap, err := Build([]byte(syntheticUpstream))
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	t.Run("first-party registration beats a reseller mirror", func(t *testing.T) {
		if got := snap.Models["gpt-4o"].Context; got != 128000 {
			t.Errorf("gpt-4o context = %d, want 128000 from the author's row, not the reseller's 999999", got)
		}
	})

	t.Run("non-chat and metadata-free rows are dropped", func(t *testing.T) {
		for _, key := range []string{"image-model", "empty-row"} {
			if _, ok := snap.Models[key]; ok {
				t.Errorf("%q survived the filter", key)
			}
		}
	})

	t.Run("alias is emitted only when it adds metadata", func(t *testing.T) {
		if got := snap.Aliases["alias-row"]; got != "gpt-4o" {
			t.Errorf("alias-row alias = %q, want gpt-4o", got)
		}
		if got, ok := snap.Aliases["reseller-only"]; ok {
			t.Errorf("reseller-only alias = %q, want none (target carries nothing extra)", got)
		}
	})
}

// Determinism is the property that makes a regeneration reviewable: the same
// payload must always produce the same bytes, even though Build ranges over Go
// maps whose iteration order the runtime randomizes per loop.
func TestBuildDeterministic(t *testing.T) {
	first, err := Build([]byte(syntheticUpstream))
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	want, err := first.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	for i := 0; i < 20; i++ {
		snap, err := Build([]byte(syntheticUpstream))
		if err != nil {
			t.Fatalf("Build() attempt %d error: %v", i, err)
		}
		got, err := snap.Marshal()
		if err != nil {
			t.Fatalf("Marshal() attempt %d error: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("Build() attempt %d is not byte-identical to the first run", i)
		}
	}
}

// The checked-in snapshot must be in exactly the form Build emits; otherwise a
// regeneration would rewrite the whole file with cosmetic churn.
func TestBuildReproducesCheckedInSnapshot(t *testing.T) {
	// Re-decoding and re-encoding the checked-in bytes exercises the same
	// canonical encoder Build uses, without shipping the 6 MB upstream file.
	data := catalog.Embedded()
	snap, err := catalog.Parse(data)
	if err != nil {
		t.Fatalf("Parse(Embedded()) error: %v", err)
	}
	got, err := snap.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("checked-in snapshot is not in canonical form; regenerate with go run ./tools/cataloggen")
	}
}

func TestBuildRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"not json", `nope`},
		{"empty object", `{}`},
		{"wrong shape", `[]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Build([]byte(tt.in)); err == nil {
				t.Fatalf("Build(%s) succeeded, want error", tt.in)
			}
		})
	}
}

func TestPrefer(t *testing.T) {
	// Every pair below is arranged so the EXPECTED winner is the candidate the
	// rule under test favours and the LEXICOGRAPHICALLY WORSE one: the truthful
	// row is "zzz-…" and the inflated, mirror or unstated row is "aaa-…". A
	// test whose intended winner also sorts first would still pass with the
	// rule deleted, which is exactly what these fixtures avoid.
	//
	// A positive-signal author: canonical names its own provider.
	author := candidate{provider: "zzz-author", model: "m", canonical: "zzz-author/m", entry: catalog.Entry{Context: 1000, Output: 100}}
	// A reseller that points canonical at the author — rank 0, loses every
	// dispute even with wildly inflated limits (and sorts first, so only the
	// rank rule can keep it out).
	mirror := candidate{provider: "aaa-reseller", model: "m", canonical: "zzz-author/m", entry: catalog.Entry{Context: 900000, Output: 9000}}
	// An author that ships no canonical at all: neutral, indistinguishable
	// from a reseller that omits it, so only the conservative limit rule
	// protects the truthful row.
	anonAuthor := candidate{provider: "zzz-author", model: "m", entry: catalog.Entry{Context: 1000, Output: 100}}
	// A reseller with no canonical and inflated claims.
	anonReseller := candidate{provider: "aaa-reseller", model: "m", entry: catalog.Entry{Context: 900000, Output: 9000}}
	// A bare canonical (no provider part) is not authorship evidence.
	bareAuthor := candidate{provider: "zzz-author", model: "m", canonical: "m", entry: catalog.Entry{Context: 1000, Output: 100}}
	// A second author with the same rank but an inflated claim, sorted first.
	authorInflated := candidate{provider: "aaa-author", model: "m", canonical: "aaa-author/m", entry: catalog.Entry{Context: 900000, Output: 9000}}
	// Stated output on one side, unstated on the other: the stated row is the
	// one that must win, and it sorts last.
	stated := candidate{provider: "zzz-author", model: "m", canonical: "zzz-author/m", entry: catalog.Entry{Context: 1000, Output: 100}}
	unstated := candidate{provider: "aaa-author", model: "m", canonical: "aaa-author/m", entry: catalog.Entry{Context: 1000}}
	// Nothing decides between these but the provider order itself.
	sameA := candidate{provider: "aaa", model: "m", canonical: "aaa/m", entry: catalog.Entry{Context: 1000, Output: 100}}
	sameB := candidate{provider: "bbb", model: "m", canonical: "bbb/m", entry: catalog.Entry{Context: 1000, Output: 100}}

	tests := []struct {
		name string
		a, b candidate
		want bool
	}{
		// Authorship rank decides first. Without the rank rule the inflated
		// mirror would win on limits, so these cases are load-bearing.
		{"author beats a mirror with inflated limits", author, mirror, true},
		{"mirror never beats the author", mirror, author, false},
		{"neutral beats a mirror", bareAuthor, mirror, true},
		{"neutral author beats a mirror even with smaller limits", anonAuthor, mirror, true},

		// Same rank: the conservative limit rule. An inflated claim must
		// never raise a limit, and an unstated one loses to a stated one.
		// Each of these would flip if the comparison were deleted.
		{"inflated reseller loses to the truthful author", anonReseller, anonAuthor, false},
		{"truthful author beats the inflated reseller", anonAuthor, anonReseller, true},
		{"inflated duplicate loses among authors", authorInflated, author, false},
		{"smaller claimed limit wins among authors", author, authorInflated, true},
		{"stated limit beats an unstated one", stated, unstated, true},
		{"unstated limit loses to a stated one", unstated, stated, false},
		// Neither side states a limit: nothing about limits decides, so the
		// provider order does.
		{"identical metadata falls back to provider order", sameA, sameB, true},
		{"identical metadata reverse", sameB, sameA, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := prefer(tt.a, tt.b); got != tt.want {
				t.Errorf("prefer(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// TestSmallestNonZeroWins pins the conservative comparison directly, including
// the case the table above cannot reach: two unstated values that differ.
func TestSmallestNonZeroWins(t *testing.T) {
	tests := []struct {
		name    string
		a, b    int
		want    bool
		decided bool
	}{
		{"smaller positive wins", 1000, 2000, true, true},
		{"larger positive loses", 2000, 1000, false, true},
		{"equal values decide nothing", 1000, 1000, false, false},
		{"unstated loses to stated", 0, 1000, false, true},
		{"stated beats unstated", 1000, 0, true, true},
		{"zero versus zero decides nothing", 0, 0, false, false},
		// Both unstated but unequal is noise, not a claim: the comparison must
		// not decide on it, so prefer falls through to its provider/model
		// tie-breaks.
		{"zero versus negative decides nothing", 0, -1, false, false},
		{"negative versus zero decides nothing", -1, 0, false, false},
		{"both negative decides nothing", -5, -1, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wins, decided := smallestNonZeroWins(tt.a, tt.b)
			if wins != tt.want || decided != tt.decided {
				t.Errorf("smallestNonZeroWins(%d, %d) = (%v, %v), want (%v, %v)", tt.a, tt.b, wins, decided, tt.want, tt.decided)
			}
		})
	}
}

// TestAuthorshipRank pins the three-way signal that replaced the old
// first-party bool: only a canonical naming the candidate's own provider is
// positive evidence, and both "no canonical" and a bare canonical stay
// neutral rather than being read as authorship.
func TestAuthorshipRank(t *testing.T) {
	tests := []struct {
		name string
		c    candidate
		want int
	}{
		{"own provider in canonical", candidate{provider: "acme", canonical: "acme/gpt-4o"}, rankAuthor},
		{"own provider, different case", candidate{provider: "Acme", canonical: "acme/gpt-4o"}, rankAuthor},
		{"no canonical", candidate{provider: "acme"}, rankNeutral},
		{"bare canonical", candidate{provider: "acme", canonical: "gpt-4o"}, rankNeutral},
		{"empty provider part", candidate{provider: "acme", canonical: "/gpt-4o"}, rankNeutral},
		{"empty model part", candidate{provider: "acme", canonical: "acme/"}, rankNeutral},
		{"somebody else's provider", candidate{provider: "reseller", canonical: "acme/gpt-4o"}, rankMirror},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := authorshipRank(tt.c); got != tt.want {
				t.Errorf("authorshipRank(%v) = %d, want %d", tt.c, got, tt.want)
			}
		})
	}
}
