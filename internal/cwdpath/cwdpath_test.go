package cwdpath

import (
	"path/filepath"
	"testing"
)

// TestCanonical pins the filter rules: empty is allowed (matches
// everything), relative paths are rejected, absolute paths are canonicalized
// equivalently regardless of spelling (compare against the same
// canonicalization instead of the raw temp path — macOS and Windows spell
// both through symlinked / junctioned parents), and a missing directory still
// canonicalizes (it must match nothing rather than error).
func TestCanonical(t *testing.T) {
	cwd := t.TempDir()
	got, err := Canonical("")
	if err != nil || got != "" {
		t.Fatalf("empty filter = (%q, %v), want (\"\", nil)", got, err)
	}
	if _, err := Canonical("relative"); err == nil {
		t.Fatal("relative filter must be rejected")
	} else if want := `cwd "relative" must be an absolute path`; err.Error() != want {
		t.Fatalf("relative filter error = %q, want %q", err.Error(), want)
	}
	got, err = Canonical(filepath.Join(cwd, "..", filepath.Base(cwd)))
	if err != nil {
		t.Fatalf("absolute filter: %v", err)
	}
	want, err := Canonical(cwd)
	if err != nil {
		t.Fatalf("reference canonicalization: %v", err)
	}
	if got != want {
		t.Fatalf("canonicalized filter = %q, want %q", got, want)
	}
	if _, err = Canonical(filepath.Join(cwd, "does-not-exist")); err != nil {
		t.Fatalf("nonexistent filter must still canonicalize: %v", err)
	}
}

// TestCanonicalStored pins the best-effort variant: canonical input is
// cleaned like Canonical, and unusable input is passed through instead of
// failing (a session is always persisted).
func TestCanonicalStored(t *testing.T) {
	cwd := t.TempDir()
	canonical, err := Canonical(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if got := CanonicalStored(cwd); got != canonical {
		t.Fatalf("CanonicalStored(%q) = %q, want %q", cwd, got, canonical)
	}
	if got := CanonicalStored(""); got != "" {
		t.Fatalf("CanonicalStored(\"\") = %q, want \"\"", got)
	}
	if got := CanonicalStored("relative"); got != "relative" {
		t.Fatalf("CanonicalStored(relative) = %q, want the raw path", got)
	}
}
