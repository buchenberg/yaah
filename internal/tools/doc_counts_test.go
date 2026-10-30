package tools

// doc_counts_test.go pins the leaf-tool count stated in architecture.md to
// the leafTools registry. The audit found the old count had drifted by one;
// deriving it here keeps the next added tool from repeating that.

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestArchitectureLeafToolCount(t *testing.T) {
	doc, err := os.ReadFile("../../docs/architecture.md")
	if err != nil {
		t.Fatalf("read architecture.md: %v", err)
	}
	want := fmt.Sprintf("%d of the\n%d leaf tools", len(leafTools)-1, len(leafTools))
	if !strings.Contains(string(doc), want) {
		t.Errorf("architecture.md does not state %q; leafTools has %d entries and one platform shell is selected", want, len(leafTools))
	}
}
