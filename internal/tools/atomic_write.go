package tools

import (
	"fmt"
	"os"
	"path/filepath"
)

// atomicWriteFile writes data to path via a temp file in the same
// directory followed by a rename, so a crash mid-write can never leave
// a truncated user file behind (review finding A4 / plan task 7.5).
// When the file already exists, its permission mode is preserved —
// perm is only applied for newly created files. The rename is atomic
// on POSIX and uses MoveFileEx(REPLACE_EXISTING) on Windows.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if st, err := os.Stat(path); err == nil {
		perm = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("atomic write: create temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("atomic write: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("atomic write: chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("atomic write: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("atomic write: rename: %w", err)
	}
	return nil
}
