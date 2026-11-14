package spec

// hash_test.go pins WD7 and the yaah-0u0v.17 acceptance: the spec hash is the
// content address of canonical JSON, so a document expressed as YAML and the
// same document expressed as JSON share one hash. A trace record can therefore
// attribute a run to its document no matter which front-end the author used.

import "testing"

const hashYAML = `apiVersion: yaah.workflow/v1
kind: Workflow
metadata:
  name: hash-stability
spec:
  params:
    task:
      type: string
  steps:
    - id: a
      kind: delegate
      role: developer
      prompt: do
`

const hashJSON = `{
  "apiVersion": "yaah.workflow/v1",
  "kind": "Workflow",
  "metadata": {"name": "hash-stability"},
  "spec": {
    "params": {"task": {"type": "string"}},
    "steps": [{"id": "a", "kind": "delegate", "role": "developer", "prompt": "do"}]
  }
}`

func TestSpecHashStableAcrossFrontends(t *testing.T) {
	yDoc, err := Parse([]byte(hashYAML))
	if err != nil {
		t.Fatalf("parse yaml: %v", err)
	}
	jDoc, err := Parse([]byte(hashJSON))
	if err != nil {
		t.Fatalf("parse json: %v", err)
	}
	yh, err := SpecHash(yDoc)
	if err != nil {
		t.Fatalf("hash yaml: %v", err)
	}
	jh, err := SpecHash(jDoc)
	if err != nil {
		t.Fatalf("hash json: %v", err)
	}
	if yh != jh {
		t.Fatalf("yaml hash %s != json hash %s", yh, jh)
	}
}

func TestSpecHashChangesWithDocument(t *testing.T) {
	a, err := Parse([]byte(hashYAML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	b, err := Parse([]byte(`apiVersion: yaah.workflow/v1
kind: Workflow
metadata:
  name: hash-stability
spec:
  steps:
    - id: a
      kind: delegate
      role: developer
      prompt: different
`))
	if err != nil {
		t.Fatalf("parse b: %v", err)
	}
	ah, err := SpecHash(a)
	if err != nil {
		t.Fatalf("hash a: %v", err)
	}
	bh, err := SpecHash(b)
	if err != nil {
		t.Fatalf("hash b: %v", err)
	}
	if ah == bh {
		t.Fatalf("distinct documents share hash %s", ah)
	}
}
