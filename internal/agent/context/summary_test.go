package context

import "testing"

func TestValidateSummary(t *testing.T) {
	tests := []struct {
		name    string
		summary string
		want    bool
	}{
		{"empty", "", false},
		{"whitespace", "   \n\t", false},
		{"unfilled template", "## Goal\n- (none)\n\n## Progress\n### Done\n- (none)\n### In Progress\n- (none)\n\n## Key Decisions\n- (none)\n\n## Next Steps\n- (none)\n\n## Critical Context\n- (none)\n\n## Relevant Files\n- (none)\n", false},
		{"placeholder stubs", "## Goal\n- [single-sentence task summary]\n\n## Next Steps\n- [ordered next actions]\n", false},
		{"bare scaffold, no bullets", "## Goal\n## Progress\n## Next Steps\n", false},
		{"one substantive bullet", "## Goal\n- (none)\n\n## Progress\n### In Progress\n- fixing the compaction validator in context_manager.go\n", true},
		{"prose without bullets", "The session migrated go_refactor to the workspace interface.\n", true},
		{"mixed placeholders and substance", "## Goal\n- (none)\n\n## Done\n- landed PR #596\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, reason := ValidateSummary(tt.summary)
			if ok != tt.want {
				t.Errorf("ValidateSummary = %v (reason %q), want %v", ok, reason, tt.want)
			}
			if !tt.want && reason == "" {
				t.Error("a rejected summary must carry a reason")
			}
		})
	}
}

func TestBoundedSummaryPrefix(t *testing.T) {
	if got := BoundedSummaryPrefix("short"); got != "short" {
		t.Errorf("short summary prefix = %q", got)
	}
	long := make([]byte, 0, summaryPrefixBytes+64)
	for i := 0; i < summaryPrefixBytes+64; i++ {
		long = append(long, 'x')
	}
	got := BoundedSummaryPrefix(string(long))
	if len(got) > summaryPrefixBytes+3 {
		t.Errorf("prefix = %d bytes, want <= %d + ellipsis", len(got), summaryPrefixBytes)
	}
}

// A single non-placeholder line is information and must not be rejected —
// this is also the shape of compaction-test stubs ("summary").
func TestValidateSummary_acceptsSingleWord(t *testing.T) {
	if ok, reason := ValidateSummary("summary"); !ok {
		t.Errorf("single-word summary rejected: %q", reason)
	}
}

func TestValidateSummary_headingVsIssueReference(t *testing.T) {
	// "#123 fixed ..." is content, not a scaffold heading.
	if ok, reason := ValidateSummary("## Progress\n- #123 fixed the login redirect\n"); !ok {
		t.Errorf("issue reference rejected as scaffold: %q", reason)
	}
}

func TestValidateSummary_placeholderVariants(t *testing.T) {
	placeholders := []string{
		"## Goal\n1. (none)\n",
		"## Progress\n- none.\n",
		"## Next Steps\n* N/A\n",
		"## Blocked\n- —\n",
		"## Done\n2) [completed work]\n",
		"## Notes\n- tbd\n",
	}
	for _, summary := range placeholders {
		t.Run(summary, func(t *testing.T) {
			if ok, _ := ValidateSummary(summary); ok {
				t.Errorf("placeholder-only summary accepted: %q", summary)
			}
		})
	}
}
