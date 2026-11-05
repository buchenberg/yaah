package wake

import (
	"strings"
	"testing"
)

func TestEvidenceRenderAndPersistence(t *testing.T) {
	code := 0
	ev := Evidence{
		Kind:     "command",
		ExitCode: &code,
		Excerpt:  "ok\n",
		Detail:   "probe",
	}
	rendered := ev.Render()
	for _, want := range []string{"command", "exit=0", "probe", "ok"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("Render() = %q, missing %q", rendered, want)
		}
	}

	got := ParseEvidence(MarshalEvidence(ev))
	if got.Kind != "command" || got.ExitCode == nil || *got.ExitCode != 0 || got.Detail != "probe" {
		t.Fatalf("round trip = %+v, want the command envelope", got)
	}

	// A row written before the envelope was introduced is plain prose.
	legacy := ParseEvidence("job bg-1 completed with a report")
	if legacy.Detail != "job bg-1 completed with a report" || legacy.Kind != "" {
		t.Fatalf("legacy evidence = %+v, want it preserved in Detail", legacy)
	}
	if MarshalEvidence(Evidence{}) != "" {
		t.Fatal("zero evidence should persist as an empty column")
	}

	// A Detail-only structured record must survive a second round trip
	// without being mistaken for legacy prose and wrapped again.
	detailOnly := Evidence{Detail: "a synthesized watch notice"}
	got = ParseEvidence(MarshalEvidence(detailOnly))
	if got.Detail != detailOnly.Detail || got.Kind != "" {
		t.Fatalf("detail-only round trip = %+v", got)
	}
	got = ParseEvidence(MarshalEvidence(got))
	if got.Detail != detailOnly.Detail || got.Kind != "" {
		t.Fatalf("detail-only second round trip = %+v", got)
	}
}

func TestEvidenceRenderFile(t *testing.T) {
	ev := Evidence{
		Kind:         "file",
		Status:       "matched",
		MatchedPaths: []string{"/w/ci-green"},
		Detail:       "1 file(s) match \"ci*\" (exists)",
	}
	rendered := ev.Render()
	for _, want := range []string{"file matched", "/w/ci-green", "1 file(s)"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("Render() = %q, missing %q", rendered, want)
		}
	}
}
