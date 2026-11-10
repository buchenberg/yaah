package permissions

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func mustParse(t *testing.T, src string) Rules {
	t.Helper()
	var r Rules
	if err := yaml.Unmarshal([]byte(src), &r); err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return r
}

func TestParsePlan(t *testing.T) {
	got := mustParse(t, `
edit: { "*.md": allow, "*": deny }
bash: { "git push*": ask, "git status*": allow }
webfetch: deny
`)
	if len(got) != 5 {
		t.Fatalf("expected 5 rules, got %d: %+v", len(got), got)
	}
	// Order is source order: the first two are edit's.
	if got[0].Tool != "edit" || got[0].Resource != "*.md" || got[0].Effect != Allow {
		t.Fatalf("rule 0 = %+v", got[0])
	}
	if got[1].Resource != "*" || got[1].Effect != Deny {
		t.Fatalf("rule 1 = %+v", got[1])
	}
	if got[4].Tool != "webfetch" || got[4].Resource != "" || got[4].Effect != Deny {
		t.Fatalf("rule 4 = %+v", got[4])
	}
}

func TestMatchLastWins(t *testing.T) {
	rules := mustParse(t, `
edit: { "*": deny, "*.md": allow }
`)
	cases := []struct {
		resource string
		want     Effect
		wantOK   bool
	}{
		{"notes.md", Allow, true},
		{"docs/notes.md", Allow, true}, // *.md crosses directories
		{"docs/deep/x.md", Allow, true},
		{"main.go", Deny, true},
		{"", Deny, true}, // "*" matches anything, including empty
		{"sub/main.go", Deny, true},
	}
	for _, tc := range cases {
		got, ok := rules.Match("edit", tc.resource)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("Match(edit, %q) = (%v, %v), want (%v, %v)", tc.resource, got, ok, tc.want, tc.wantOK)
		}
	}
	if _, ok := rules.Match("bash", "ls"); ok {
		t.Error("unlisted tool must not match")
	}
}

func TestMatchToolNameOnly(t *testing.T) {
	rules := mustParse(t, "webfetch: deny\n")
	if got, ok := rules.Match("webfetch", ""); !ok || got != Deny {
		t.Errorf("bare effect must match any resource, got (%v, %v)", got, ok)
	}
}

func TestMatchEmptyResourceAgainstGlob(t *testing.T) {
	// A narrowing glob cannot match a call with no resource: the call
	// cannot be shown to be in the glob's scope, so only name-only rules
	// catch it.
	rules := mustParse(t, "edit: { \"*.md\": allow }\n")
	if _, ok := rules.Match("edit", ""); ok {
		t.Error("glob rule must not match an empty resource")
	}
}

func TestRejectUnknownEffect(t *testing.T) {
	for _, src := range []string{"edit: {\"*.md\": allo}\n", "webfetch: deney\n"} {
		var r Rules
		if err := yaml.Unmarshal([]byte(src), &r); err == nil {
			t.Errorf("unknown effect must fail parse: %q", src)
		}
	}
}

func TestRejectNonMapping(t *testing.T) {
	var r Rules
	if err := yaml.Unmarshal([]byte("[1,2]\n"), &r); err == nil {
		t.Error("sequence form must fail")
	}
}

func TestResourceForDomains(t *testing.T) {
	cases := []struct {
		tool, args, want string
	}{
		{"bash", `{"command":"git push origin main"}`, "git push origin main"},
		{"powershell", `{"command":"Get-ChildItem"}`, "Get-ChildItem"},
		{"edit", `{"filePath":"docs/x.md","oldString":"a"}`, "docs/x.md"},
		{"write", `{"filePath":"main.go","content":"x"}`, "main.go"},
		{"patch", `{"filePath":"a.go","patch":"..."}`, "a.go"},
		{"move", `{"path":"docs/a.md","to":"docs/b.md"}`, "docs/a.md"}, // source wins
		{"rmdir", `{"path":".git/objects"}`, ".git/objects"},
		{"bulk", `{"pattern":"*.tmp","path":"tmp"}`, "tmp"},
		{"webfetch", `{"url":"https://evil.example/x"}`, "https://evil.example/x"},
		{"read", `{"filePath":"main.go"}`, "main.go"}, // read-only tools resource-match too
		{"grep", `{"pattern":"x"}`, ""},               // no target arg: name-only
		{"bash", `not json`, ""},
	}
	for _, tc := range cases {
		if got := ResourceFor(tc.tool, tc.args); got != tc.want {
			t.Errorf("ResourceFor(%s, %s) = %q, want %q", tc.tool, tc.args, got, tc.want)
		}
	}
}

func TestWindowsPathGlob(t *testing.T) {
	rules := mustParse(t, "edit: { \"docs/*.md\": deny }\n")
	if _, ok := rules.Match("edit", `docs\notes.md`); !ok {
		t.Error("backslash path must match forward-slash glob")
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	src := `
edit: { "*.md": allow, "*": deny }
bash: { "git push*": ask, "git status*": allow }
webfetch: deny
`
	parsed := mustParse(t, src)
	out, err := yaml.Marshal(parsed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	reparsed := mustParse(t, string(out))
	if len(reparsed) != len(parsed) {
		t.Fatalf("round trip changed rule count: %d -> %d", len(parsed), len(reparsed))
	}
	for i := range parsed {
		if parsed[i] != reparsed[i] {
			t.Errorf("rule %d: %+v -> %+v", i, parsed[i], reparsed[i])
		}
	}
}
