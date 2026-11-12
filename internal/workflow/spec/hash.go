package spec

import (
	"crypto/sha256"
	"encoding/hex"
)

// SpecHash returns the stable SHA-256 hex digest of the document's
// canonical JSON. The hash is recorded in workflow traces so a run can be
// attributed to the exact spec text that produced it (WD7). Equivalent
// YAML and JSON documents always share a hash.
func SpecHash(doc *Document) (string, error) {
	canonical, err := CanonicalJSON(doc)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}
