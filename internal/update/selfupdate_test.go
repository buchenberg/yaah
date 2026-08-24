package update

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyChecksum(t *testing.T) {
	dir := t.TempDir()
	asset := filepath.Join(dir, "yaah-test-amd64")
	content := []byte("fake binary content")
	if err := os.WriteFile(asset, content, 0o755); err != nil {
		t.Fatal(err)
	}

	sum := sha256.Sum256(content)
	good := hex.EncodeToString(sum[:])

	checksums := []byte(strings.Join([]string{
		"0000000000000000000000000000000000000000000000000000000000000000  other-asset",
		good + "  yaah-test-amd64",
		"",
	}, "\n"))

	if err := VerifyChecksum(asset, checksums); err != nil {
		t.Errorf("valid checksum rejected: %v", err)
	}

	t.Run("mismatch fails closed", func(t *testing.T) {
		bad := []byte("1111111111111111111111111111111111111111111111111111111111111111  yaah-test-amd64")
		err := VerifyChecksum(asset, bad)
		if err == nil {
			t.Fatal("mismatched checksum accepted")
		}
		if !strings.Contains(err.Error(), "mismatch") {
			t.Errorf("error = %q; want mismatch reason", err)
		}
	})

	t.Run("missing entry fails closed", func(t *testing.T) {
		err := VerifyChecksum(asset, []byte("abc  unrelated-file\n"))
		if err == nil {
			t.Fatal("missing checksum entry accepted")
		}
		if !strings.Contains(err.Error(), "no SHA-256 checksum entry") {
			t.Errorf("error = %q; want missing-entry reason", err)
		}
	})

	t.Run("uppercase digest accepted", func(t *testing.T) {
		upper := []byte(strings.ToUpper(good) + "  yaah-test-amd64")
		if err := VerifyChecksum(asset, upper); err != nil {
			t.Errorf("uppercase digest rejected: %v", err)
		}
	})

	t.Run("tampered asset fails", func(t *testing.T) {
		tampered := filepath.Join(dir, "tampered")
		if err := os.WriteFile(tampered, []byte("tampered content"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := VerifyChecksum(tampered, checksums); err == nil {
			t.Fatal("asset without checksum entry accepted")
		}
	})
}

func TestEscapePSPath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", `C:\bin\yaah.exe`, `C:\bin\yaah.exe`},
		{"double quote", `C:\we"ird\yaah.exe`, "C:\\we`\"ird\\yaah.exe"},
		{"subexpression", `C:\$(calc)\yaah.exe`, "C:\\`$(calc)\\yaah.exe"},
		{"backtick", "C:\\we`ird\\yaah.exe", "C:\\we``ird\\yaah.exe"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := escapePSPath(tt.in); got != tt.want {
				t.Errorf("escapePSPath(%q) = %q; want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestLookupChecksum(t *testing.T) {
	checksums := []byte("abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234  dir/yaah-linux-amd64\nnot a digest line\n")
	if got := lookupChecksum(checksums, "yaah-linux-amd64"); got != "abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234" {
		t.Errorf("lookupChecksum = %q", got)
	}
	if got := lookupChecksum(checksums, "missing"); got != "" {
		t.Errorf("lookupChecksum(missing) = %q; want empty", got)
	}
	// A short non-64-char hex token must not be treated as a digest.
	if got := lookupChecksum([]byte("deadbeef  yaah-x\n"), "yaah-x"); got != "" {
		t.Errorf("short digest accepted: %q", got)
	}
}
