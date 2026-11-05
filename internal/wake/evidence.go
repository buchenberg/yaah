package wake

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Evidence is the bounded, typed result of one predicate evaluation. It is
// the machine-readable half; Render() is the model/UI half. Every field is
// capped by the engine before it reaches here (excerpts 2 KiB, matches five
// paths, detail/excerpt strings bounded), so a digest cannot grow without
// bound.
type Evidence struct {
	Kind         string   `json:"kind,omitempty"`
	Status       string   `json:"status,omitempty"`
	ExitCode     *int     `json:"exit_code,omitempty"`
	MatchedPaths []string `json:"matched_paths,omitempty"`
	Excerpt      string   `json:"excerpt,omitempty"`
	Result       string   `json:"result,omitempty"`
	Detail       string   `json:"detail,omitempty"`
}

// IsZero reports whether there is nothing to show.
func (e Evidence) IsZero() bool {
	return e.Kind == "" && e.Status == "" && e.ExitCode == nil &&
		len(e.MatchedPaths) == 0 && e.Excerpt == "" && e.Result == "" && e.Detail == ""
}

// Render is the human/model-readable projection used by the wake digest and
// the watch list. It is deliberately plain text: the structured fields remain
// available on the result object.
func (e Evidence) Render() string {
	var lines []string
	head := e.Kind
	if e.Status != "" {
		if head != "" {
			head += " "
		}
		head += e.Status
	}
	if e.ExitCode != nil {
		head += fmt.Sprintf(" exit=%d", *e.ExitCode)
	}
	if len(e.MatchedPaths) > 0 {
		if head != "" {
			head += ": "
		}
		head += strings.Join(e.MatchedPaths, ", ")
	}
	if head != "" {
		lines = append(lines, head)
	}
	if e.Detail != "" {
		lines = append(lines, e.Detail)
	}
	if e.Result != "" {
		lines = append(lines, "result: "+e.Result)
	}
	if e.Excerpt != "" {
		lines = append(lines, e.Excerpt)
	}
	return strings.Join(lines, "\n")
}

// ParseEvidence decodes a persisted evidence column. A row written by the
// prose-era format (plain text, not JSON) falls back to Detail so an upgrade
// does not drop an already-fired watch's evidence.
func ParseEvidence(raw string) Evidence {
	if strings.TrimSpace(raw) == "" {
		return Evidence{}
	}
	var ev Evidence
	if err := json.Unmarshal([]byte(raw), &ev); err == nil && ev.Kind != "" {
		return ev
	}
	return Evidence{Detail: raw}
}

// MarshalEvidence is the persistence encoding for a WatchRecord.
func MarshalEvidence(ev Evidence) string {
	if ev.IsZero() {
		return ""
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return ""
	}
	return string(raw)
}
