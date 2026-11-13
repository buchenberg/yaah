package validate

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/buchenberg/yaah/internal/workflow/ir"
)

// ------------------------------------------------------------------ fixtures

// authorityTestEnv is the synthetic host the rule tables are checked
// against: one delegatable role, one primary-only persona, one role that
// exists but is not delegatable, and no entry at all for "ghost".
func authorityTestEnv() Env {
	return Env{
		Roles: map[string]Role{
			"developer":    {Delegatable: true},
			"orchestrator": {PrimaryOnly: true},
			"intern":       {},
		},
		Worlds:         []string{"host", "container"},
		DefaultWorld:   "workspace",
		HostNetwork:    ir.NetworkAllowAll,
		BrokerHosts:    []string{"api.github.com"},
		MaxDispatches:  64,
		MaxPromptBytes: 8 << 10,
	}
}

// authorityGraph resolves a steps block into a graph, the same discipline
// findingsOf applies to the structural family: these rules speak about a
// graph, so a document that never became one is a broken test rather than
// a finding.
func authorityGraph(t *testing.T, steps string) *ir.Graph {
	t.Helper()
	g, diags := Validate(documentWith(steps))
	if g == nil {
		t.Fatalf("document did not resolve, cannot exercise the authority rules:%s", render(diags))
	}
	return g
}

// requireCode asserts that exactly one finding carries code, at want.
func requireCode(t *testing.T, diags []Diagnostic, code, want string) {
	t.Helper()
	var hits []Diagnostic
	for _, d := range diags {
		if d.Code == code {
			hits = append(hits, d)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("%s findings = %d, want 1; got:%s", code, len(hits), render(diags))
	}
	if hits[0].Path != want {
		t.Fatalf("%s anchored at %s, want %s", code, hits[0].Path, want)
	}
}

// requireSilent asserts that no finding carries code.
func requireSilent(t *testing.T, diags []Diagnostic, code string) {
	t.Helper()
	for _, d := range diags {
		if d.Code == code {
			t.Fatalf("unexpected %s:%s", code, render(diags))
		}
	}
}

// ------------------------------------------------------------------- WF030

func TestDispatchRoles(t *testing.T) {
	cases := []struct {
		name  string
		steps string
		env   Env
		want  string // "" for silent
		msg   string
	}{
		{
			name:  "unknown role",
			steps: "  steps:\n    - {id: a, kind: delegate, role: ghost, prompt: hi}\n",
			want:  "steps.a.role",
			msg:   "not in the host role registry",
		},
		{
			name:  "primary-only persona",
			steps: "  steps:\n    - {id: a, kind: delegate, role: orchestrator, prompt: hi}\n",
			want:  "steps.a.role",
			msg:   "primary persona",
		},
		{
			name:  "exists but not delegatable",
			steps: "  steps:\n    - {id: a, kind: delegate, role: intern, prompt: hi}\n",
			want:  "steps.a.role",
			msg:   "not delegatable",
		},
		{
			name:  "no role named",
			steps: "  steps:\n    - {id: a, kind: delegate, prompt: hi}\n",
			want:  "steps.a.role",
			msg:   "must name a role",
		},
		{
			name: "gate judge role",
			steps: "  steps:\n    - id: g\n      kind: gate\n      judge:\n        role: orchestrator\n        prompt: which is best\n" +
				"    - id: h\n      kind: delegate\n      role: developer\n      prompt: hi\n",
			want: "steps.g.judge.role",
			msg:  "primary persona",
		},
		{
			name: "judge names nothing",
			steps: "  steps:\n    - id: s\n      kind: select\n      judge: {role: \"\", prompt: pick one}\n" +
				"    - {id: h, kind: delegate, role: developer, prompt: hi}\n",
			want: "steps.s.judge.role",
			msg:  "must name a role",
		},
		{
			name:  "delegatable role is clean",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi}\n",
		},
		{
			name:  "empty registry adjudicates no names",
			steps: "  steps:\n    - {id: a, kind: delegate, role: whatever, prompt: hi}\n",
			env:   Env{Roles: map[string]Role{}},
		},
		{
			name:  "empty registry still refuses an unnamed dispatch",
			steps: "  steps:\n    - {id: a, kind: delegate, prompt: hi}\n",
			env:   Env{Roles: map[string]Role{}},
			want:  "steps.a.role",
			msg:   "must name a role",
		},
		{
			name:  "a settle dispatches no role",
			steps: "  steps:\n    - {id: a, kind: settle, action: apply}\n",
		}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := tc.env
			if env.Roles == nil {
				env = authorityTestEnv()
			}
			diags := Authority(authorityGraph(t, tc.steps), env)
			if tc.want == "" {
				requireSilent(t, diags, CodeWF030)
				return
			}
			requireCode(t, diags, CodeWF030, tc.want)
			if d := findingAt(t, diags, CodeWF030, tc.want); !strings.Contains(d.Message, tc.msg) {
				t.Errorf("message %q does not mention %q", d.Message, tc.msg)
			}
		})
	}
}

// ------------------------------------------------------------------- WF031

func TestFilesystemGrants(t *testing.T) {
	cases := []struct {
		name  string
		steps string
		env   Env
		want  string
		msg   string
	}{
		{
			name:  "absolute grant",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi, grants: [{path: /etc/passwd, access: readonly}]}\n",
			want:  "steps.a.grants[0]",
			msg:   "not world-relative",
		},
		{
			name:  "escaping grant",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi, grants: [{path: ../../secrets, access: readwrite}]}\n",
			want:  "steps.a.grants[0]",
			msg:   "not world-relative",
		},
		{
			name:  "drive-letter grant",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi, grants: [{path: 'C:/Users/vault', access: readwrite}]}\n",
			want:  "steps.a.grants[0]",
			msg:   "not world-relative",
		},
		{
			name:  "whole-world grant",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi, grants: [{path: ., access: readwrite}]}\n",
			want:  "steps.a.grants[0]",
			msg:   "not world-relative",
		},
		{
			name: "overlapping grants",
			steps: "  steps:\n    - id: a\n      kind: delegate\n      role: developer\n      prompt: hi\n      grants:\n" +
				"        - {path: src, access: readwrite}\n        - {path: src/main.go, access: readonly}\n",
			want: "steps.a.grants",
			msg:  "silently wins",
		},
		{
			name: "identical grants",
			steps: "  steps:\n    - id: a\n      kind: delegate\n      role: developer\n      prompt: hi\n      grants:\n" +
				"        - {path: src, access: readonly}\n        - {path: src, access: readonly}\n",
			want: "steps.a.grants",
			msg:  "overlap",
		},
		{
			name: "disjoint grants",
			steps: "  steps:\n    - id: a\n      kind: delegate\n      role: developer\n      prompt: hi\n      grants:\n" +
				"        - {path: src, access: readwrite}\n        - {path: docs, access: readonly}\n",
		},
		{
			name: "segment-boundary near misses",
			steps: "  steps:\n    - id: a\n      kind: delegate\n      role: developer\n      prompt: hi\n      grants:\n" +
				"        - {path: cmd, access: readwrite}\n        - {path: cmdr, access: readonly}\n" +
				"        - {path: src/../docs, access: readonly}\n",
		},
		{
			name: "undeclared world",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi, world: vm}\n" +
				"    - {id: b, kind: delegate, role: developer, prompt: hi, world: host}\n",
			want: "steps.a.world",
			msg:  "not one the host declares",
		},
		{
			name:  "no declared worlds is unchecked",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi, world: vm}\n",
			env:   Env{Roles: authorityTestEnv().Roles},
		},
		{
			name: "escaped artifact path",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi}\n" +
				"    - {id: r, kind: emit, value: done, path: /tmp/leak.md}\n",
			want: "steps.r.path",
			msg:  "not world-relative",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := tc.env
			if env.Roles == nil {
				env = authorityTestEnv()
			}
			diags := Authority(authorityGraph(t, tc.steps), env)
			if tc.want == "" {
				requireSilent(t, diags, CodeWF031)
				return
			}
			requireCode(t, diags, CodeWF031, tc.want)
			if d := findingAt(t, diags, CodeWF031, tc.want); !strings.Contains(d.Message, tc.msg) {
				t.Errorf("message %q does not mention %q", d.Message, tc.msg)
			}
		})
	}
}

// TestDefaultsGrantsReportPerDispatch pins the consequence of folding:
// resolution rewrites spec.defaults.grants onto every dispatch, so a bad
// default is reported once per dispatch instead of at the block an author
// wrote. The count is the contract — the per-dispatch path is what the
// runtime would actually have refused.
func TestDefaultsGrantsReportPerDispatch(t *testing.T) {
	g := authorityGraph(t, "  defaults:\n    grants:\n      - {path: ../outside, access: readwrite}\n  steps:\n"+
		"    - {id: a, kind: delegate, role: developer, prompt: hi}\n"+
		"    - {id: b, kind: delegate, role: developer, prompt: yo}\n")
	diags := Authority(g, authorityTestEnv())
	var hits []string
	for _, d := range diags {
		if d.Code == CodeWF031 {
			hits = append(hits, d.Path)
		}
	}
	if want := []string{"steps.a.grants[0]", "steps.b.grants[0]"}; strings.Join(hits, " ") != strings.Join(want, " ") {
		t.Errorf("WF031 paths = %v, want %v", hits, want)
	}
}

func TestFilesystemGrantsOnHandBuiltGraph(t *testing.T) {
	g := &ir.Graph{Steps: []*ir.Node{
		{
			Path: "steps.a", Kind: ir.KindDelegate, Role: "developer",
			Grants: []ir.Grant{
				{Path: "src", Access: ir.GrantAccess("execute")},
				{Path: "", Access: ir.AccessReadOnly},
			},
		},
	}}
	diags := Authority(g, authorityTestEnv())
	// Two findings at one node, so this asserts presence rather than the
	// exclusivity requireCode gives the single-finding cases.
	findingAt(t, diags, CodeWF031, "steps.a.grants[0].access")
	findingAt(t, diags, CodeWF031, "steps.a.grants[1]")
}

// ------------------------------------------------------------------- WF032

func TestNetworkPolicy(t *testing.T) {
	env := authorityTestEnv()
	cases := []struct {
		name  string
		steps string
		env   Env
		want  string
		msg   string
	}{
		{
			name:  "broker without hosts",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi, network: broker}\n",
			env:   func() Env { e := env; e.BrokerHosts = nil; return e }(),
			want:  "steps.a.network",
			msg:   "no hosts to broker",
		},
		{
			name:  "broker with a configured allowlist",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi, network: broker}\n",
			env:   env,
		},
		{
			name:  "widening past the host ceiling",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi, network: allow_all}\n",
			env:   func() Env { e := env; e.HostNetwork = ir.NetworkDenyAll; return e }(),
			want:  "steps.a.network",
			msg:   "wider than the host ceiling",
		},
		{
			name:  "broker is wider than a deny_all ceiling",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi, network: broker}\n",
			env:   func() Env { e := env; e.HostNetwork = ir.NetworkDenyAll; return e }(),
			want:  "steps.a.network",
			msg:   "wider than the host ceiling",
		},
		{
			name:  "narrowing is allowed",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi, network: deny_all}\n",
			env:   func() Env { e := env; e.HostNetwork = ir.NetworkBroker; return e }(),
		},
		{
			name:  "no host ceiling means no comparison",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi, network: allow_all}\n",
			env:   func() Env { e := env; e.HostNetwork = ""; return e }(),
		},
		{
			name:  "unset network inherits the host default",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi}\n",
			env:   func() Env { e := env; e.HostNetwork = ir.NetworkDenyAll; return e }(),
		},
		{
			name:  "a container states no network",
			steps: "  steps:\n    - {id: f, kind: fanout, over: [a], max_parallel: 1, body: {kind: delegate, role: developer, prompt: hi}}\n",
			env:   func() Env { e := env; e.HostNetwork = ir.NetworkDenyAll; return e }(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := Authority(authorityGraph(t, tc.steps), tc.env)
			if tc.want == "" {
				requireSilent(t, diags, CodeWF032)
				return
			}
			requireCode(t, diags, CodeWF032, tc.want)
			if d := findingAt(t, diags, CodeWF032, tc.want); !strings.Contains(d.Message, tc.msg) {
				t.Errorf("message %q does not mention %q", d.Message, tc.msg)
			}
		})
	}
}

// ------------------------------------------------------------------- WF036

func TestInlineSecrets(t *testing.T) {
	cases := []struct {
		name  string
		steps string
		want  string
		msg   string
	}{
		{
			name:  "private key",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: 'load -----BEGIN RSA PRIVATE KEY----- and sign'}\n",
			want:  "steps.a.prompt",
			msg:   "private key block",
		},
		{
			name:  "bearer line",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: 'Authorization: Bearer abc123'}\n",
			want:  "steps.a.prompt",
			msg:   "authorization line",
		},
		{
			name:  "aws key",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: 'use AKIAIOSFODNN7EXAMPLE'}\n",
			want:  "steps.a.prompt",
			msg:   "AWS access key id",
		},
		{
			name:  "github token",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: 'ghp_abcdefghijklmnopqrstuvwxyz123456'}\n",
			want:  "steps.a.prompt",
			msg:   "GitHub token",
		},
		{
			name:  "slack token",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: 'post with xoxb-1234'}\n",
			want:  "steps.a.prompt",
			msg:   "Slack token",
		},
		{
			name:  "sk- key at a word start",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: 'key: sk-abcdef123456'}\n",
			want:  "steps.a.prompt",
			msg:   "API key of sk- form",
		},
		{
			name:  "emit text carries it too",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi}\n    - {id: r, kind: emit, value: 'gho_abcdefghijklmnop'}\n",
			want:  "steps.r.value",
			msg:   "GitHub token",
		},
		{
			name:  "judge prompt",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi}\n    - {id: s, kind: select, judge: {role: developer, prompt: 'xoxp-9999'}}\n",
			want:  "steps.s.judge.prompt",
			msg:   "Slack token",
		},
		{
			name: "call parameter constant",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi}\n" +
				"    - {id: c, kind: call, workflow: other, params: {token: 'sk-zzzz1111'}}\n",
			want: "steps.c.params.token",
			msg:  "API key of sk- form",
		},
		{
			name: "assert argument",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: hi}\n" +
				"    - {id: g, kind: gate, assert: {name: check, args: {key: 'AKIAQQQQWWWW'}}}\n",
			want: "steps.g.assert.args.key",
			msg:  "AWS access key id",
		},
		{
			name:  "a reference to a secret is not a secret",
			steps: "  params:\n    api_key:\n      type: string\n  steps:\n    - {id: a, kind: delegate, role: developer, prompt: 'sign with {{ params.api_key }}'}\n",
		},
		{
			name: "prose about credentials stays clean",
			steps: "  steps:\n    - {id: a, kind: delegate, role: developer, prompt: 'Ask the API for the task-lists; the header is injected by the broker.'}\n" +
				"    - {id: b, kind: delegate, role: developer, prompt: 'Shaking out risk-analysis is normal work.'}\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diags := Authority(authorityGraph(t, tc.steps), authorityTestEnv())
			if tc.want == "" {
				requireSilent(t, diags, CodeWF036)
				return
			}
			requireCode(t, diags, CodeWF036, tc.want)
			if d := findingAt(t, diags, CodeWF036, tc.want); !strings.Contains(d.Message, tc.msg) {
				t.Errorf("message %q does not mention %q", d.Message, tc.msg)
			}
		})
	}
}

// TestInlineSecretsReportsEachMarkerOnce pins both halves of the dedup:
// one finding per marker per field, and one finding per distinct marker.
func TestInlineSecretsReportsEachMarkerOnce(t *testing.T) {
	g := authorityGraph(t, "  steps:\n    - id: a\n      kind: delegate\n      role: developer\n      prompt: |\n"+
		"        first ghp_aaa second ghp_bbb third AKIAIOSFODNN7EXAMPLE\n")
	diags := Authority(g, authorityTestEnv())
	var hits []Diagnostic
	for _, d := range diags {
		if d.Code == CodeWF036 {
			hits = append(hits, d)
		}
	}
	if len(hits) != 2 {
		t.Fatalf("WF036 findings = %d, want 2 (GitHub token once, AWS key once); got:%s", len(hits), render(hits))
	}
	if !strings.Contains(hits[0].Message, "AWS") || !strings.Contains(hits[1].Message, "GitHub") {
		t.Errorf("markers out of table order: %q, %q", hits[0].Message, hits[1].Message)
	}
}

// ------------------------------------------------------------------- union

func TestAuthorityUnion(t *testing.T) {
	env := authorityTestEnv()
	unreach := &ir.Node{
		Path: "steps.ghost", Kind: ir.KindDelegate, Role: "ghost", Needs: []string{"nowhere"},
		Grants: []ir.Grant{{Path: "secrets", Access: ir.AccessReadWrite}},
	}
	g := &ir.Graph{Steps: []*ir.Node{
		{Path: "steps.a", Kind: ir.KindDelegate, Role: "developer",
			Grants: []ir.Grant{{Path: "src", Access: ir.AccessReadWrite}}, Network: ir.NetworkBroker},
		{Path: "steps.b", Kind: ir.KindDelegate, Role: "developer",
			Grants: []ir.Grant{{Path: "./src/", Access: ir.AccessReadOnly}}, Network: ir.NetworkDenyAll},
		{Path: "steps.d", Kind: ir.KindEmit, ArtifactPath: "out/report.md"},
		unreach,
	}}
	union := authorityUnion(g, env)

	want := []PathGrant{{World: "workspace", Path: "src", Access: ir.AccessReadWrite}}
	if len(union.Grants) != len(want) || union.Grants[0] != want[0] {
		t.Errorf("Grants = %+v, want %+v (merged at the wider access, ./src normalised onto src)", union.Grants, want)
	}
	if union.Unreachable != 1 {
		t.Errorf("Unreachable = %d, want 1", union.Unreachable)
	}
	if got, want := modeList(union.NetworkModes), "deny_all broker"; got != want {
		t.Errorf("NetworkModes = %q, want %q narrowest first", got, want)
	}
	if strings.Join(union.Hosts, " ") != "api.github.com" {
		t.Errorf("Hosts = %v, want the configured broker allowlist", union.Hosts)
	}
	if union.OpenNetwork {
		t.Error("OpenNetwork = true, want false: no reachable dispatch may address any host")
	}
	if strings.Join(union.Artifacts, " ") != "out/report.md" {
		t.Errorf("Artifacts = %v", union.Artifacts)
	}
	for _, gr := range union.Grants {
		if gr.Path == "secrets" {
			t.Errorf("an unreachable grant leaked into the union: %+v", union.Grants)
		}
	}

	// The rules still speak about the unreachable node even though the
	// union will not credit it.
	if !hasCode(Authority(g, env), CodeWF030) {
		t.Error("authority rules skipped the unreachable node")
	}
}

func TestAuthorityUnionInheritsHostNetwork(t *testing.T) {
	g := &ir.Graph{Steps: []*ir.Node{
		{Path: "steps.a", Kind: ir.KindDelegate, Role: "developer"},
	}}
	union := authorityUnion(g, authorityTestEnv())
	if !union.OpenNetwork {
		t.Error("OpenNetwork = false; an unset network inherits the host allow_all default")
	}
	if len(union.NetworkModes) != 1 || union.NetworkModes[0] != ir.NetworkAllowAll {
		t.Errorf("NetworkModes = %v, want [allow_all]", union.NetworkModes)
	}
	if len(union.Hosts) != 0 {
		t.Errorf("Hosts = %v, want empty: allow_all is not bounded by the allowlist", union.Hosts)
	}
}

func TestAuthorityUnionKeepsWorldsApart(t *testing.T) {
	g := &ir.Graph{Steps: []*ir.Node{
		{Path: "steps.a", Kind: ir.KindDelegate, Role: "developer", World: "host",
			Grants: []ir.Grant{{Path: "src", Access: ir.AccessReadOnly}}},
		{Path: "steps.b", Kind: ir.KindDelegate, Role: "developer", World: "container",
			Grants: []ir.Grant{{Path: "src", Access: ir.AccessReadWrite}}},
	}}
	union := authorityUnion(g, authorityTestEnv())
	if len(union.Grants) != 2 {
		t.Fatalf("Grants = %+v, want one entry per world", union.Grants)
	}
	if union.Grants[0].World != "container" || union.Grants[1].World != "host" {
		t.Errorf("Grants not sorted by world: %+v", union.Grants)
	}
}

// ----------------------------------------------------------------- aggregates

func TestAuthorityNilGraph(t *testing.T) {
	if got := Authority(nil, authorityTestEnv()); got != nil {
		t.Errorf("Authority(nil) = %v, want nil", got)
	}
}

// TestAuthorityTerminatesOnCyclicBody is the alias guard the package
// philosophy demands: ir.Graph is exported and editable, and a rule family
// that recursed through a self-containing body would hang the CLI.
func TestAuthorityTerminatesOnCyclicBody(t *testing.T) {
	n := &ir.Node{Path: "steps.a", Kind: ir.KindFanout}
	n.Body = []*ir.Node{n}
	g := &ir.Graph{Steps: []*ir.Node{n}}
	if got := Authority(g, authorityTestEnv()); len(got) != 0 {
		t.Errorf("Authority on a self-containing body = %s, want none", render(got))
	}
}

// TestEmissionIsDocumentOrder pins the determinism contract: the same graph
// walked twice yields the same sequence, and nodes report in document order
// within a rule.
func TestEmissionIsDocumentOrder(t *testing.T) {
	g := authorityGraph(t, "  steps:\n    - {id: a, kind: delegate, role: ghost, prompt: hi}\n"+
		"    - {id: b, kind: delegate, role: ghost, prompt: yo}\n")
	env := authorityTestEnv()
	first := Authority(g, env)
	second := Authority(g, env)
	if codeList(first) != codeList(second) {
		t.Fatalf("Authority is not deterministic: %q vs %q", codeList(first), codeList(second))
	}
	var paths []string
	for _, d := range first {
		if d.Code == CodeWF030 {
			paths = append(paths, d.Path)
		}
	}
	if got, want := strings.Join(paths, " "), "steps.a.role steps.b.role"; got != want {
		t.Errorf("WF030 paths = %q, want %q", got, want)
	}
}

// modeList renders network modes for comparison in failure messages.
func modeList(ms []ir.NetworkMode) string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = string(m)
	}
	return strings.Join(out, " ")
}

// authorityGoldenEnv is the synthetic host the accepted corpus is checked
// against: every role the goldens name, and otherwise the least the host
// can configure. Ceilings are zero — not configured — so the corpus is
// proof that the rules decline the axes the host never set, and the only
// dispatch bounds being honoured are the two the goldens declare for
// themselves (bounded_repair 12, fanout_settle 24).
func authorityGoldenEnv() Env {
	return Env{
		Roles: map[string]Role{
			"developer": {Delegatable: true},
			"planner":   {Delegatable: true},
			"reviewer":  {Delegatable: true},
			"tester":    {Delegatable: true},
		},
		DefaultWorld:   "workspace",
		MaxPromptBytes: 64 << 10,
	}
}

func TestAuthorityAndCostGoldenCorpusHasNoFindings(t *testing.T) {
	patterns := []string{
		filepath.Join("..", "ir", "testdata", "specs", "*.yaml"),
		filepath.Join("..", "spec", "testdata", "valid", "*.yaml"),
	}
	var files []string
	for _, p := range patterns {
		matches, err := filepath.Glob(p)
		if err != nil {
			t.Fatalf("glob %s: %v", p, err)
		}
		files = append(files, matches...)
	}
	sort.Strings(files)
	if len(files) == 0 {
		t.Fatal("no workflow documents matched the authority corpus")
	}
	env := authorityGoldenEnv()
	for _, path := range files {
		t.Run(filepath.Base(filepath.Dir(path))+"-"+filepath.Base(path), func(t *testing.T) {
			data := readFixtureFile(t, path)
			g, diags := Validate(data)
			if g == nil {
				t.Fatalf("document did not resolve:%s", render(diags))
			}
			if got := Authority(g, env); len(got) != 0 {
				t.Errorf("authority findings:%s", render(got))
			}
			if got := Cost(g, env); len(got) != 0 {
				t.Errorf("cost findings:%s", render(got))
			}
			if union := authorityUnion(g, env); union.Unreachable != 0 {
				t.Errorf("%d nodes were left out of the union as unreachable", union.Unreachable)
			}
		})
	}
}
