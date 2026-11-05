package tools

import (
	"context"
	"io"
	"os"
)

// ReadFileLimit reads at most max bytes from path. It is the bounded-reader
// seam for tools that only inspect a prefix (the wake engine's file
// predicates), so a large file is not materialized on every poll.
//
// It lives beside workspace.go rather than in it because the host syscall
// layer is the one place in this package allowed to call os.*: keeping the
// primitive next to the Workspace implementation makes the exemption in
// enforcement_test.go honest. Substrates that cannot bound a read simply do
// not implement it; the caller falls back to a full read plus a cap.
func (w *localWorkspace) ReadFileLimit(_ context.Context, path string, max int) ([]byte, error) {
	if max <= 0 {
		return []byte{}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, int64(max)))
}
