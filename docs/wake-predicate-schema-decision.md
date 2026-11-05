# Decision: wait/watch predicate shape

**Status:** Accepted (string form retained as a sanctioned exception)
**Bead:** `yaah-95ri.13`
**Date:** 2026-11-05

## Question

`wait_for` and `watch` describe a predicate as two strings:

```json
{"on": "file:tmp/ci-green", "until": "contains:green"}
{"on": "job:bg-2"}
{"on": "command:gh pr checks 42", "until": "exit_zero"}
```

Should the tools instead expose a structured schema — a `source` enum, a
`target` string, and a typed `condition` — so the schema owns the vocabulary
rather than a parser and the tool docs?

## Constraints

- **Token budget.** Every tool schema is in every request. A structured
  condition needs a nested object with an enum per source (`exit`, `status`,
  `regex`, `contains`, `exists`), or a union of condition objects. The compact
  form is one short string per field.
- **Model ergonomics.** Models already compose `job:<id>`, `time:30s` and a
  regex naturally. A structured form asks the model to translate the same
  intent into a nested object with source-specific keys, which is more places
  to get wrong.
- **Validation.** The runtime parser is the authority either way: a structured
  schema can say `status` is an integer but cannot say it must be 100–599, nor
  that `command:` accepts `exit_zero` while `file:` accepts `exists`.
- **Repo rule.** The doc-parity tests require the schema to own argument
  semantics and the docs to state behavior. The current split is the reverse:
  the schema says "a predicate string" and the parser/docs own the vocabulary.

## Options

| Option | Decision | Reason |
|---|---|---|
| Fully structured schema (`source`/`target`/`condition`) | **No, for v1** | Adds nested schema tokens and source-specific condition unions to every request; the parser still has to validate the semantic edges. No observed failure justifies it. |
| Two accepted forms (string and structured) | **No** | Two sources of truth for the same predicate, two validation paths, and a model may mix them. |
| String `on`/`until` with a documented grammar | **Adopt** | Compact, natural for models, one parser, and errors that name the valid forms. The doc rule is satisfied by recording the exception explicitly rather than by pretending the string is self-describing. |
| Opaque object blob | **No** | Worse than both: no schema guidance and no readable model-facing shape. |

## Decision

Keep the string form, and make the trade-off explicit:

1. `on` is `<source>:<target>`; `until` is the source-specific condition. The
   parser in `internal/wake/spec.go` is the single authority for both.
2. The schema descriptions name the sources and the condition vocabulary, and
   `wait_for.md` / `watch.md` state the grammar. Runtime errors name the valid
   forms (for example, `until "status:200" is not valid for a command
   predicate`).
3. The repository's "schema owns argument semantics" rule has a recorded
   exception for this pair of tools. This document is that exception; a future
   change to a structured shape should replace it, not add a second path.
4. The parser rejects a malformed predicate before a watch is armed or a wait
   starts, so a typo fails the call rather than burning a timeout.

## Why not structured now

The compact form has been the shape since the first implementation and has no
measured misuse yet. The structured alternative is a larger prompt surface for
a vocabulary that is still small (six sources, one optional condition) and
whose edges are semantic, not type-shaped. The main cost of the current form —
the docs must carry the grammar — is already paid and tested by the doc-parity
suite.

## Revisit criteria

Revisit this decision when any of these holds:

- tool-call telemetry shows a meaningful rate of malformed `on`/`until` calls
  (parse errors per `wait_for`/`watch` call);
- the predicate set grows enough that a flat source list is hard to discover
  (for example, adding nested/boolean predicates);
- an MCP or ACP client needs to construct predicates programmatically and
  asks for a typed shape;
- a second consumer needs the predicate as data rather than a model-facing
  tool argument.

The revisit should produce a structured schema and migrate the tool docs and
tests in one change, not accept both forms indefinitely.
