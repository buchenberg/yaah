package tools

import "fmt"

// OverwriteRefusedError reports a blind overwrite: the path already holds
// content and the caller did not pass overwrite: true (yaah-5hkb).
//
// It names what would be lost and the three ways out, because the caller
// usually has the wrong mental model rather than the wrong file — a name
// collision on a new file, a partial change that belongs in edit, or a genuine
// replace that needs the flag.
type OverwriteRefusedError struct {
	Path  string
	Bytes int
	Lines int
}

func (e *OverwriteRefusedError) Error() string {
	return fmt.Sprintf(
		"%s already exists (%d bytes, %d lines) and write replaces a file wholesale, so nothing was changed; pass overwrite: true to replace it (the previous content is stashed and its path reported), use edit or patch for a partial change, or write to a different path",
		e.Path, e.Bytes, e.Lines)
}

// DigestMismatchError reports that the caller's if_digest precondition did not
// hold: the file on disk is not the content the caller believed it had read.
//
// It is deliberately separate from StaleWriteError, which reports the guard's
// own compare-and-swap losing to an out-of-band writer mid-call. This one is
// about the caller's snapshot being older than the file; the remedies differ,
// and the message says which digest was wanted and which was found.
type DigestMismatchError struct {
	Path     string
	Expected string
	Found    string
}

func (e *DigestMismatchError) Error() string {
	return fmt.Sprintf(
		"%s: if_digest does not match (expected %s, found sha256:%s); the file on disk is not the content you read — re-read it and retry, or drop if_digest if you meant to replace it as it is",
		e.Path, e.Expected, shortFileDigest(e.Found))
}
