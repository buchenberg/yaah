package permissions

import "testing"

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, value string
		want           bool
	}{
		{"*", "main.go", true},
		{"*", "docs/notes.md", true}, // * crosses separators (gitignore-style)
		{"*.md", "notes.md", true},
		{"*.md", "docs/notes.md", true},    // the plan's example depends on this
		{"docs/**", "docs/notes.md", true}, // ** accepted as an alias
		{"docs/*.md", "docs/notes.md", true},
		{"docs/*.md", "lib/main.go", false},
		{"git push*", "git push origin main", true},
		{"git.push*", "git push origin", false}, // regex metachars literal
		{"file?.md", "file1.md", true},
		{"file?.md", "file12.md", false},
		{"[", "anything", false},         // invalid pattern matches nothing
		{`docs\x.md`, `docs\x.md`, true}, // backslash path normalized both sides
	}
	for _, tc := range cases {
		if got := globMatch(tc.pattern, tc.value); got != tc.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tc.pattern, tc.value, got, tc.want)
		}
	}
}
