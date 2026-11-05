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
	first := candidate{provider: "acme", model: "m", entry: catalog.Entry{Context: 1000, Output: 100}}
	mirror := candidate{provider: "zzz-reseller", model: "m", canonical: "acme/m", entry: catalog.Entry{Context: 900000, Output: 9000}}
	wider := candidate{provider: "acme-x", model: "m", entry: catalog.Entry{Context: 2000, Output: 100}}
	narrower := candidate{provider: "acme-a", model: "m", entry: catalog.Entry{Context: 1000, Output: 50}}
	sameA := candidate{provider: "aaa", model: "m", entry: catalog.Entry{Context: 1000, Output: 100}}
	sameB := candidate{provider: "bbb", model: "m", entry: catalog.Entry{Context: 1000, Output: 100}}

	tests := []struct {
		name string
		a, b candidate
		want bool
	}{
		{"first party beats a wider mirror", first, mirror, true},
		{"mirror never beats first party", mirror, first, false},
		{"wider context wins among first parties", wider, first, true},
		{"wider output wins on equal context", first, narrower, true},
		{"narrower output loses on equal context", narrower, first, false},
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
