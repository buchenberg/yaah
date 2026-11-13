package spec

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func readFixture(t *testing.T, rel string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return data
}

func TestValidFixtures(t *testing.T) {
	for _, name := range []string{"sequential", "fanout_settle", "bounded_repair", "speculative"} {
		t.Run(name, func(t *testing.T) {
			yamlBytes := readFixture(t, filepath.Join("valid", name+".yaml"))
			jsonBytes := readFixture(t, filepath.Join("valid", name+".json"))

			docY, err := ParseYAML(yamlBytes)
			if err != nil {
				t.Fatalf("ParseYAML: %v", err)
			}
			docJ, err := ParseJSON(jsonBytes)
			if err != nil {
				t.Fatalf("ParseJSON: %v", err)
			}
			// Parse sniffs the format; both routes must agree.
			docSniffed, err := Parse(yamlBytes)
			if err != nil {
				t.Fatalf("Parse(yaml): %v", err)
			}
			if !reflect.DeepEqual(docY, docJ) {
				t.Fatal("YAML and JSON twins decode to different documents")
			}
			if !reflect.DeepEqual(docY, docSniffed) {
				t.Fatal("Parse disagrees with ParseYAML")
			}

			canon, err := CanonicalJSON(docY)
			if err != nil {
				t.Fatalf("CanonicalJSON: %v", err)
			}
			canonJ, err := CanonicalJSON(docJ)
			if err != nil {
				t.Fatalf("CanonicalJSON(json twin): %v", err)
			}
			if !bytes.Equal(canon, canonJ) {
				t.Fatal("YAML and JSON twins produce different canonical JSON")
			}

			goldenPath := filepath.Join("testdata", "golden", name+".canonical.json")
			if *update {
				if err := os.WriteFile(goldenPath, canon, 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
			}
			golden := readFixture(t, filepath.Join("golden", name+".canonical.json"))
			if !bytes.Equal(canon, golden) {
				t.Fatalf("canonical JSON drifted from golden\nwant:\n%s\ngot:\n%s", golden, canon)
			}

			hash, err := SpecHash(docY)
			if err != nil {
				t.Fatalf("SpecHash: %v", err)
			}
			hashJ, err := SpecHash(docJ)
			if err != nil {
				t.Fatalf("SpecHash(json twin): %v", err)
			}
			if hash != hashJ {
				t.Fatalf("hash mismatch across formats: %s vs %s", hash, hashJ)
			}
			sum := sha256.Sum256(canon)
			if want := hex.EncodeToString(sum[:]); hash != want {
				t.Fatalf("SpecHash %s does not match sha256(canonical) %s", hash, want)
			}
			// Stability: reparse and hash again.
			docAgain, err := ParseYAML(yamlBytes)
			if err != nil {
				t.Fatalf("reparse: %v", err)
			}
			hashAgain, err := SpecHash(docAgain)
			if err != nil {
				t.Fatalf("SpecHash(reparse): %v", err)
			}
			if hashAgain != hash {
				t.Fatalf("hash unstable: %s vs %s", hash, hashAgain)
			}
		})
	}
}

func TestJSONSchemaGolden(t *testing.T) {
	schema, err := JSONSchema()
	if err != nil {
		t.Fatalf("JSONSchema: %v", err)
	}
	goldenPath := filepath.Join("testdata", "golden", "schema.golden.json")
	if *update {
		if err := os.WriteFile(goldenPath, schema, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	golden := readFixture(t, filepath.Join("golden", "schema.golden.json"))
	if !bytes.Equal(schema, golden) {
		t.Fatalf("JSON schema drifted from golden\nwant:\n%s\ngot:\n%s", golden, schema)
	}
}

func TestInvalidFixtures(t *testing.T) {
	cases := []struct {
		name     string
		file     string
		wantPath string
		wantMsg  string
	}{
		{"unknown node field", "unknown_field.yaml", "spec.steps[0].kolect", `unknown field "kolect"`},
		{"unknown root field", "unknown_field_root.json", "spec.stepz", `unknown field "stepz"`},
		{"wrong apiVersion", "wrong_apiversion.yaml", "apiVersion", `unknown APIVersion "yaah.workflow/v1alpha1"`},
		{"missing apiVersion", "missing_apiversion.yaml", "apiVersion", `missing required field "apiVersion"`},
		{"unknown kind", "unknown_kind.yaml", "spec.steps[0].kind", `unknown node kind "banana"`},
		{"malformed selector shorthand", "malformed_selector.yaml", "spec.steps[0].over", "got number"},
		{"malformed output shorthand", "malformed_output.yaml", "spec.steps[0].output", "got list"},
		{"unknown settle action", "bad_action.yaml", "spec.steps[1].action", `unknown SettleAction "merge"`},
		{"unknown param type", "bad_param_type.yaml", "spec.params.task.type", `unknown ParamType "str"`},
		{"template depth bomb", "template_depth.yaml", "spec.steps[0].prompt", "exceeds limit 8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := readFixture(t, filepath.Join("invalid", tc.file))
			doc, err := Parse(data)
			if err == nil {
				t.Fatal("Parse succeeded, want failure")
			}
			if doc != nil {
				t.Fatal("Parse returned a document alongside an error")
			}
			var de *DecodeError
			if !errors.As(err, &de) {
				t.Fatalf("error is %T, want *DecodeError: %v", err, err)
			}
			if de.Path != tc.wantPath {
				t.Errorf("path = %q, want %q", de.Path, tc.wantPath)
			}
			if !strings.Contains(de.Message, tc.wantMsg) {
				t.Errorf("message %q does not contain %q", de.Message, tc.wantMsg)
			}
		})
	}
}

func TestParseLimits(t *testing.T) {
	t.Run("oversized document", func(t *testing.T) {
		var buf bytes.Buffer
		buf.WriteString(fmt.Sprintf("apiVersion: %s\nkind: Workflow\nmetadata: {name: big}\nspec:\n  steps:\n    - id: a\n      kind: emit\n      value: x\n", APIVersionV1))
		for buf.Len() <= MaxSpecBytes {
			buf.WriteString("# padding line to exceed the document size limit\n")
		}
		_, err := Parse(buf.Bytes())
		var de *DecodeError
		if !errors.As(err, &de) {
			t.Fatalf("error is %T, want *DecodeError: %v", err, err)
		}
		if !strings.Contains(de.Message, fmt.Sprintf("limit %d bytes", MaxSpecBytes)) {
			t.Errorf("message %q does not mention the %d byte limit", de.Message, MaxSpecBytes)
		}
	})

	t.Run("oversized prompt", func(t *testing.T) {
		src := fmt.Sprintf("apiVersion: %s\nkind: Workflow\nmetadata: {name: big-prompt}\nspec:\n  steps:\n    - id: a\n      kind: delegate\n      role: worker\n      prompt: %s\n",
			APIVersionV1, strings.Repeat("a", MaxPromptBytes+1))
		_, err := Parse([]byte(src))
		var de *DecodeError
		if !errors.As(err, &de) {
			t.Fatalf("error is %T, want *DecodeError: %v", err, err)
		}
		if de.Path != "spec.steps[0].prompt" {
			t.Errorf("path = %q", de.Path)
		}
	})

	t.Run("too many nodes", func(t *testing.T) {
		var buf bytes.Buffer
		buf.WriteString(fmt.Sprintf("apiVersion: %s\nkind: Workflow\nmetadata: {name: many}\nspec:\n  steps:\n", APIVersionV1))
		for i := 0; i <= MaxNodes; i++ {
			fmt.Fprintf(&buf, "    - id: n%03d\n      kind: emit\n      value: x\n", i)
		}
		_, err := Parse(buf.Bytes())
		var de *DecodeError
		if !errors.As(err, &de) {
			t.Fatalf("error is %T, want *DecodeError: %v", err, err)
		}
		if !strings.Contains(de.Message, fmt.Sprintf("limit is %d", MaxNodes)) {
			t.Errorf("message %q does not mention the %d node limit", de.Message, MaxNodes)
		}
	})

	t.Run("yaml timestamp rejected", func(t *testing.T) {
		src := fmt.Sprintf("apiVersion: %s\nkind: Workflow\nmetadata: {name: dates}\nspec:\n  params:\n    when:\n      type: string\n      default: 2026-01-02\n  steps:\n    - id: a\n      kind: emit\n      value: x\n", APIVersionV1)
		_, err := Parse([]byte(src))
		var de *DecodeError
		if !errors.As(err, &de) || !strings.Contains(de.Message, "timestamp") {
			t.Fatalf("error = %v, want timestamp rejection", err)
		}
	})
}

func TestShorthandForms(t *testing.T) {
	src := fmt.Sprintf(`apiVersion: %s
kind: Workflow
metadata: {name: shorthands}
spec:
  steps:
    - id: root
      kind: delegate
      role: planner
      output: Plan
      prompt: plan
    - id: spread
      kind: fanout
      over: steps.root.output.workstreams
      as: ws
      body:
        kind: delegate
        role: developer
        prompt: "work on {{ ws }}"
        output: proposal
      collect: work
    - id: pick
      kind: select
      candidates: steps.spread.collect
      policy: first_success
      output: winner
`, APIVersionV1)
	doc, err := ParseYAML([]byte(src))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	steps := doc.Spec.Steps
	if got := steps[0].Output; got == nil || got.Name != "Plan" || got.Type != "" {
		t.Errorf("output shorthand = %+v, want {Name: Plan}", got)
	}
	if got := steps[1].Over; got == nil || got.Selector == nil || got.Selector.From != "steps.root.output.workstreams" || got.Values != nil {
		t.Errorf("over shorthand = %+v, want selector", got)
	}
	if len(steps[1].Body) != 1 || steps[1].Body[0].Kind != KindDelegate {
		t.Errorf("single-node body = %+v, want one delegate", steps[1].Body)
	}
	if got := steps[2].Candidates; got == nil || got.From != "steps.spread.collect" {
		t.Errorf("candidates shorthand = %+v", got)
	}
}

func TestOverLiteralList(t *testing.T) {
	src := fmt.Sprintf(`apiVersion: %s
kind: Workflow
metadata: {name: literals}
spec:
  steps:
    - id: candidates
      kind: fanout
      over: [conservative, idiomatic, minimal]
      as: style
      body:
        kind: emit
        value: "{{ style }}"
`, APIVersionV1)
	doc, err := ParseYAML([]byte(src))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	over := doc.Spec.Steps[0].Over
	if over == nil || over.Selector != nil {
		t.Fatalf("over = %+v, want literal list", over)
	}
	want := []any{"conservative", "idiomatic", "minimal"}
	if !reflect.DeepEqual(over.Values, want) {
		t.Errorf("values = %v, want %v", over.Values, want)
	}
}

func TestAllNodeKindsDecode(t *testing.T) {
	src := fmt.Sprintf(`apiVersion: %s
kind: Workflow
metadata: {name: kinds}
spec:
  steps:
    - id: root
      kind: delegate
      role: worker
      prompt: go
      provider: anthropic
      model: claude-sonnet-4-5
      max_turns: 8
      network: broker
      world: worktree
      grants:
        - { path: src, access: readwrite }
      timeout_seconds: 600
      on_failure: continue
      seed_from: sealed-snapshot
    - id: repair
      kind: delegate
      role: developer
      prompt: fix
      needs: [root]
      continue_from: "{{ steps.root.output.proposal_id }}"
      fork_from: "{{ steps.root.output.proposal_id }}"
    - id: split
      kind: branch
      on: { from: steps.root.output.mode }
      cases:
        - { value: fast, then: quick }
        - { value: deep, then: root }
      default: quick
    - id: quick
      kind: loop
      max_iterations: 3
      body:
        kind: emit
        value: "tick {{ index }}"
      until: { from: steps.quick.collect, exists: true }
      collect: ticks
    - id: gather
      kind: aggregate
      inputs: [steps.quick.collect]
      strategy: concat
    - id: call_sub
      kind: call
      workflow: shared/inner
      params:
        task: "{{ params.task }}"
      max_depth: 2
    - id: ask
      kind: human
      prompt: Approve?
      on_timeout: skip
`, APIVersionV1)
	doc, err := ParseYAML([]byte(src))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	if len(doc.Spec.Steps) != 7 {
		t.Fatalf("steps = %d, want 7", len(doc.Spec.Steps))
	}
	for i, kind := range []NodeKind{KindDelegate, KindDelegate, KindBranch, KindLoop, KindAggregate, KindCall, KindHuman} {
		if doc.Spec.Steps[i].Kind != kind {
			t.Errorf("steps[%d].kind = %s, want %s", i, doc.Spec.Steps[i].Kind, kind)
		}
	}
}

func TestNumericCanonicalEquivalence(t *testing.T) {
	yamlSrc := fmt.Sprintf(`apiVersion: %s
kind: Workflow
metadata: {name: numbers}
spec:
  params:
    ratio:
      type: number
      default: 4.0
  steps:
    - id: a
      kind: gate
      assert:
        name: within
        args: {threshold: 0.5, count: 3}
`, APIVersionV1)
	jsonSrc := fmt.Sprintf(`{"apiVersion":%[1]q,"kind":"Workflow","metadata":{"name":"numbers"},"spec":{"params":{"ratio":{"type":"number","default":4.0}},"steps":[{"id":"a","kind":"gate","assert":{"name":"within","args":{"threshold":0.5,"count":3}}}]}}`,
		string(APIVersionV1))
	docY, err := ParseYAML([]byte(yamlSrc))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	docJ, err := ParseJSON([]byte(jsonSrc))
	if err != nil {
		t.Fatalf("ParseJSON: %v", err)
	}
	if !reflect.DeepEqual(docY, docJ) {
		t.Fatal("numeric twins decode differently")
	}
	canon, err := CanonicalJSON(docY)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	for _, want := range []string{`"default":4`, `"threshold":0.5`, `"count":3`} {
		if !bytes.Contains(canon, []byte(want)) {
			t.Errorf("canonical JSON missing %s:\n%s", want, canon)
		}
	}
	if bytes.Contains(canon, []byte("4.0")) {
		t.Errorf("canonical JSON contains non-canonical 4.0:\n%s", canon)
	}
}

func TestDuplicateYAMLKeyFails(t *testing.T) {
	src := fmt.Sprintf("apiVersion: %s\nkind: Workflow\nmetadata:\n  name: a\n  name: b\nspec:\n  steps:\n    - id: x\n      kind: emit\n      value: v\n", APIVersionV1)
	if _, err := Parse([]byte(src)); err == nil {
		t.Fatal("duplicate YAML key accepted")
	}
}

func TestJSONTrailingDataFails(t *testing.T) {
	src := fmt.Sprintf(`{"apiVersion":%[1]q,"kind":"Workflow","metadata":{"name":"x"},"spec":{"steps":[]}} trailing`, string(APIVersionV1))
	_, err := Parse([]byte(src))
	if err == nil || !strings.Contains(err.Error(), "trailing data") {
		t.Fatalf("err = %v, want trailing-data failure", err)
	}
}

func TestRequiredStepID(t *testing.T) {
	src := fmt.Sprintf("apiVersion: %s\nkind: Workflow\nmetadata: {name: anon}\nspec:\n  steps:\n    - kind: emit\n      value: x\n", APIVersionV1)
	_, err := Parse([]byte(src))
	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("error is %T, want *DecodeError: %v", err, err)
	}
	if de.Path != "spec.steps[0].id" {
		t.Errorf("path = %q, want spec.steps[0].id", de.Path)
	}
}

func TestNullRequiredFieldRejected(t *testing.T) {
	src := fmt.Sprintf("apiVersion: %s\nkind: Workflow\nmetadata: {name: nullish}\nspec:\n  steps:\n    - id: a\n      kind: emit\n      value: x\n      retry: null\n", APIVersionV1)
	doc, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("null optional field rejected: %v", err)
	}
	if doc.Spec.Steps[0].Retry != nil {
		t.Fatal("null retry should decode to nil")
	}
	bad := fmt.Sprintf("apiVersion: %s\nkind: Workflow\nmetadata: {name: nullish}\nspec:\n  steps:\n    - id: a\n      kind: null\n      value: x\n", APIVersionV1)
	_, err = Parse([]byte(bad))
	var de *DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("error is %T, want *DecodeError: %v", err, err)
	}
	if de.Path != "spec.steps[0].kind" {
		t.Errorf("path = %q, want spec.steps[0].kind", de.Path)
	}
}

func TestTemplateDepthAcceptsOrdinaryTemplates(t *testing.T) {
	src := fmt.Sprintf(`apiVersion: %s
kind: Workflow
metadata: {name: ordinary}
spec:
  steps:
    - id: a
      kind: delegate
      role: worker
      prompt: |
        Work on {{ item.brief }} ({{ index }} of {{ params.total }})
`, APIVersionV1)
	doc, err := ParseYAML([]byte(src))
	if err != nil {
		t.Fatalf("ParseYAML: %v", err)
	}
	if depth := templateDepth(doc.Spec.Steps[0].Prompt); depth != 1 {
		t.Errorf("templateDepth = %d, want 1", depth)
	}
}
