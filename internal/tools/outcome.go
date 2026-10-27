package tools

import "strings"

// Outcome describes what a settle or lifecycle verb actually did, in four
// orthogonal fields rather than a single one-word status string.
//
//   - Work: what happened to the files (integrated, destroyed, kept, …)
//   - Scope: which tree or paths were affected
//   - Record: what was persisted so the result is retryable (ids, paths, hashes)
//   - Notes: caveats the caller must not ignore
//
// A verb that has nothing to say in a field leaves it empty rather than
// inventing content, so the rendered form never lies by omission.
type Outcome struct {
	Work   string
	Scope  string
	Record string
	Notes  string
}

// String renders the outcome as a single multi-line block. Empty fields are
// omitted. The block is meant to be embedded in a tool's JSON envelope, so it
// deliberately avoids any trailing newline.
func (o Outcome) String() string {
	var b strings.Builder
	writeOutcomeLine(&b, "work", o.Work)
	writeOutcomeLine(&b, "scope", o.Scope)
	writeOutcomeLine(&b, "record", o.Record)
	writeOutcomeLine(&b, "notes", o.Notes)
	return strings.TrimRight(b.String(), "\n")
}

func writeOutcomeLine(b *strings.Builder, label, val string) {
	if val == "" {
		return
	}
	b.WriteString(label)
	b.WriteString(": ")
	b.WriteString(val)
	b.WriteByte('\n')
}
