package yaah

// The --json report schemas live here as constants and are published under
// docs/schemas/. Drift discipline (the internal/rolefile pattern):
// workflow_schema_test.go pins that both constants parse as JSON and that
// the published copies are byte-identical to the constants, so the
// documented contract cannot fall behind the shipped one.

// WorkflowValidateSchemaJSON is the published JSON Schema for
// "yaah workflow validate --json" (kind yaah.workflow.validate.v1).
const WorkflowValidateSchemaJSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://yaah.dev/schemas/workflow-validate.schema.json",
  "title": "yaah workflow validate --json report",
  "description": "JSON report emitted by 'yaah workflow validate --json <spec>' (cmd/yaah/workflow_report.go, kind yaah.workflow.validate.v1). The shape is stable within v1. errors and warnings serialize as [] (never null). A report always goes to stdout when --json is set — including load failures — with the summary line on stderr. Exit code is 0 when ok is true and 1 otherwise.",
  "type": "object",
  "additionalProperties": true,
  "required": ["kind", "spec", "status", "ok", "strict", "errors", "warnings"],
  "properties": {
    "kind": {
      "const": "yaah.workflow.validate.v1",
      "description": "Report kind discriminator; bumped only on a breaking shape change."
    },
    "spec": {
      "type": "string",
      "description": "The spec path exactly as given on the command line."
    },
    "name": {
      "type": "string",
      "description": "Workflow name from the document (omitted when the document never decoded)."
    },
    "status": {
      "enum": ["valid", "invalid", "unresolved"],
      "description": "The document's verdict, independent of --strict: valid = decoded, resolved, and no error findings; invalid = decoded and resolved into a graph but carries error findings (status=invalid always implies a non-empty errors array); unresolved = load, decode, param, or ir.Resolve failed, so node-level families (WF002-WF036) could not run and the failure is reported as a synthetic diagnostic."
    },
    "ok": {
      "type": "boolean",
      "description": "Final verdict after --strict promotion: true iff no errors and (no warnings or not strict). --strict flips ok and the exit code; it never rewrites status."
    },
    "strict": {
      "type": "boolean",
      "description": "Whether --strict was set."
    },
    "errors": {
      "type": "array",
      "description": "Error-severity findings, canonical rule order. Every error code is document-intrinsic — true on every host (WF001-WF029, WF034, WF036).",
      "items": { "$ref": "#/$defs/diagnostic" }
    },
    "warnings": {
      "type": "array",
      "description": "Warning-severity findings: the host-relative codes WF030 (role registry), WF031 (world registry; its document-intrinsic halves stay warnings for whole-code consistency — the code is the contract), WF032 (network ceiling), WF033 (dispatch budget), WF035 (configured caps). --strict promotes them to failure via ok and the exit code without moving them out of this array.",
      "items": { "$ref": "#/$defs/diagnostic" }
    }
  },
  "$defs": {
    "diagnostic": {
      "type": "object",
      "additionalProperties": true,
      "required": ["code", "message"],
      "properties": {
        "code": {
          "type": "string",
          "pattern": "^WF[0-9]{3}$",
          "description": "Stable rule code from the internal/workflow/validate catalog (see its doc.go)."
        },
        "path": {
          "type": "string",
          "description": "Dotted spec path the finding points at (omitted for document-level findings)."
        },
        "message": {
          "type": "string",
          "description": "Human-readable finding text."
        }
      }
    }
  }
}
`

// WorkflowExplainSchemaJSON is the published JSON Schema for
// "yaah workflow explain --json" (kind yaah.workflow.explain.v1).
const WorkflowExplainSchemaJSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://yaah.dev/schemas/workflow-explain.schema.json",
  "title": "yaah workflow explain --json report",
  "description": "JSON report emitted by 'yaah workflow explain --json <spec>' (cmd/yaah/workflow_report.go, kind yaah.workflow.explain.v1). Everything the text report prints, machine-readable: resolved graph, per-node facts, Mermaid DAG, parameters, worst-case repetition bounds, cost bounds, authority union, and the settlement proof for proposal-producing nodes. The shape is stable within v1 and byte-identical across runs for the same document. The report is host-relative: host, cost, and the host-relative rule families reflect the machine and configuration the command ran under; --param overrides make the report concrete for parameterized specs.",
  "type": "object",
  "additionalProperties": true,
  "required": ["kind", "spec", "status", "ok", "strict", "host", "params", "graph", "nodes", "edges", "mermaid", "fanout", "cost", "authority", "settlement", "errors", "warnings"],
  "properties": {
    "kind": { "const": "yaah.workflow.explain.v1" },
    "spec": { "type": "string", "description": "Spec path as given on the command line." },
    "name": { "type": "string", "description": "Workflow name from the document." },
    "status": {
      "enum": ["valid", "invalid", "unresolved"],
      "description": "The document's verdict, independent of --strict; status=invalid always implies a non-empty errors array. unresolved = load, decode, param, or ir.Resolve failed and the node-level families could not run."
    },
    "ok": { "type": "boolean", "description": "Final verdict after --strict promotion. --strict flips ok and the exit code; it never rewrites status." },
    "strict": { "type": "boolean", "description": "Whether --strict was set." },
    "host": { "$ref": "#/$defs/env" },
    "params": {
      "type": "array",
      "description": "Declared workflow parameters with their resolved values.",
      "items": { "$ref": "#/$defs/param" }
    },
    "graph": {
      "type": "object",
      "additionalProperties": true,
      "required": ["steps", "nodes", "unreachable_nodes"],
      "properties": {
        "steps": { "type": "integer", "description": "Top-level step count." },
        "nodes": { "type": "integer", "description": "Total resolved node count (including container bodies)." },
        "unreachable_nodes": { "type": "integer", "description": "Nodes not reachable from the entry set (WF004 territory)." }
      }
    },
    "nodes": {
      "type": "array",
      "description": "Per-node facts in canonical order.",
      "items": { "$ref": "#/$defs/node" }
    },
    "edges": {
      "type": "array",
      "description": "Resolved DAG edges.",
      "items": {
        "type": "object",
        "additionalProperties": true,
        "required": ["from", "to", "via"],
        "properties": {
          "from": { "type": "string" },
          "to": { "type": "string" },
          "via": {
            "enum": ["needs", "body", "case", "default", "on_pass", "on_fail"],
            "description": "The six step-reference kinds the vocabulary has: needs (dependency), body (container to its body), case/default (branch arms), on_pass/on_fail (gate dispositions)."
          }
        }
      }
    },
    "mermaid": { "type": "string", "description": "The resolved DAG as a Mermaid flowchart (containment as subgraphs)." },
    "fanout": {
      "type": "object",
      "additionalProperties": true,
      "required": ["max_items", "max_nodes", "containers"],
      "properties": {
        "max_items": { "type": "integer", "description": "Widest declared parallel fan-out in the document (0 when nothing fans out). Loop containers are sequential repetition and contribute no parallel width." },
        "max_nodes": { "type": "array", "items": { "type": "string" }, "description": "Node paths attaining max_items." },
        "containers": {
          "type": "array",
          "items": { "$ref": "#/$defs/repetitionContainer" },
          "description": "Every repetition container — fanout and loop — with its declared bound."
        }
      }
    },
    "cost": { "$ref": "#/$defs/cost" },
    "authority": {
      "type": "object",
      "additionalProperties": true,
      "required": ["grants", "artifacts", "hosts", "network_modes", "open_network", "unreachable_nodes"],
      "properties": {
        "grants": {
          "type": "array",
          "description": "Union of every grant any reachable node declares.",
          "items": {
            "type": "object",
            "additionalProperties": true,
            "required": ["path", "access"],
            "properties": {
              "world": { "type": "string", "description": "World the grant applies in (omitted for world-agnostic grants)." },
              "path": { "type": "string" },
              "access": { "enum": ["readonly", "readwrite"] }
            }
          }
        },
        "artifacts": { "type": "array", "items": { "type": "string" }, "description": "Union of declared emit artifacts." },
        "hosts": { "type": "array", "items": { "type": "string" }, "description": "Union of requested hosts." },
        "network_modes": {
          "type": "array",
          "items": { "enum": ["deny_all", "broker", "allow_all"] },
          "description": "Union of network modes over reachable nodes, in the IR's own spelling."
        },
        "open_network": { "type": "boolean", "description": "True when any reachable node can reach the open network." },
        "unreachable_nodes": { "type": "integer", "description": "Count excluded from the union because unreachable (authority judges all nodes; the summary counts reachable-only)." }
      }
    },
    "settlement": {
      "type": "object",
      "additionalProperties": true,
      "required": ["proposals", "open_at_exit"],
      "properties": {
        "proposals": {
          "type": "array",
          "description": "One entry per proposal-producing boundary, from the validator's settlement proof (Prove).",
          "items": { "$ref": "#/$defs/proposal" }
        },
        "open_at_exit": {
          "type": "array",
          "items": { "type": "string" },
          "description": "Producers that can reach workflow exit with a proposal still unsettled. Never null: [] means the proof found no leak."
        },
        "producers_without_record": {
          "type": "array",
          "items": { "type": "string" },
          "description": "Proposal producers the proof carries no settlement record for."
        }
      }
    },
    "errors": {
      "type": "array",
      "description": "Error-severity findings. Every error code is document-intrinsic — true on every host (WF001-WF029, WF034, WF036).",
      "items": { "$ref": "#/$defs/diagnostic" }
    },
    "warnings": {
      "type": "array",
      "description": "Warning-severity findings: host-relative codes WF030 (role registry), WF031 (world registry; document-intrinsic halves stay warnings for whole-code consistency), WF032 (network ceiling), WF033 (dispatch budget), WF035 (configured caps).",
      "items": { "$ref": "#/$defs/diagnostic" }
    }
  },
  "$defs": {
    "diagnostic": {
      "type": "object",
      "additionalProperties": true,
      "required": ["code", "message"],
      "properties": {
        "code": { "type": "string", "pattern": "^WF[0-9]{3}$" },
        "path": { "type": "string" },
        "message": { "type": "string" }
      }
    },
    "env": {
      "type": "object",
      "additionalProperties": true,
      "description": "Host facts the report is relative to.",
      "properties": {
        "roles_loaded": { "type": "boolean", "description": "Whether a role registry was readable." },
        "worlds": { "type": "array", "items": { "type": "string" }, "description": "Worlds this host declares." },
        "default_world": { "type": "string", "description": "World a dispatch without world: lands in." },
        "host_network": { "enum": ["deny_all", "broker", "allow_all", ""], "description": "The host's network ceiling." },
        "broker_hosts": { "type": "array", "items": { "type": "string" }, "description": "Host names the broker admits." },
        "seconds_per_dispatch": { "type": "integer", "description": "Host default wall-clock per dispatch used for pricing." },
        "max_turns_per_dispatch": { "type": "integer", "description": "Host default turn budget used for pricing." }
      }
    },
    "param": {
      "type": "object",
      "additionalProperties": true,
      "required": ["name", "type", "source"],
      "properties": {
        "name": { "type": "string" },
        "type": {
          "enum": ["string", "integer", "number", "boolean", "list", "map"],
          "description": "Declared parameter type in the decoder's own spelling."
        },
        "required": { "type": "boolean" },
        "source": { "enum": ["param", "default", "unset"], "description": "param = set via --param; default = the document's declared default; unset = required and missing." },
        "value": { "description": "Resolved value (omitted when unset)." }
      }
    },
    "node": {
      "type": "object",
      "additionalProperties": true,
      "required": ["path", "kind"],
      "properties": {
        "path": { "type": "string", "description": "Canonical dotted node path." },
        "id": { "type": "string" },
        "kind": { "type": "string", "description": "One of the eleven IR kinds: delegate, fanout, branch, loop, gate, settle, select, aggregate, call, emit, human." },
        "needs": { "type": "array", "items": { "type": "string" } },
        "role": { "type": "string" },
        "lane": {
          "enum": ["report", "proposal", ""],
          "description": "Dispatch lane. Only delegate-kind dispatches have one; shared is a world value, not a lane. Omitted for kinds that carry no lane."
        },
        "network": {
          "enum": ["deny_all", "broker", "allow_all", ""],
          "description": "Effective network mode. Host-substituted values only appear for kinds that execute a payload or judge (delegate, fanout, loop, gate, select, call); control-flow and artifact kinds (branch, aggregate, collect, emit, settle, human) leave it empty rather than state an authority they never exercise."
        },
        "world": { "type": "string", "description": "Effective world, with the same substitution policy as network." },
        "world_inherited": { "type": "boolean", "description": "True when the world column is the host default rather than the document's choice (shown with * in text)." },
        "network_inherited": { "type": "boolean", "description": "True when the network column is the host ceiling rather than the document's choice (shown with * in text). The two columns inherit independently." },
        "grants": {
          "type": "array",
          "items": {
            "type": "object",
            "additionalProperties": true,
            "required": ["path", "access"],
            "properties": {
              "path": { "type": "string" },
              "access": { "enum": ["readonly", "readwrite"] }
            }
          }
        },
        "notes": { "type": "array", "items": { "type": "string" }, "description": "Renderer annotations (over/as/collect for containers, retries, effective settle policy with its default marking)." },
        "produces_proposal": { "type": "boolean" }
      }
    },
    "repetitionContainer": {
      "type": "object",
      "additionalProperties": true,
      "required": ["path", "kind", "items"],
      "properties": {
        "path": { "type": "string" },
        "kind": { "enum": ["fanout", "loop"], "description": "fanout bounds parallel width and contributes to max_items; loop bounds sequential iterations and contributes none." },
        "max_parallel": { "type": ["integer", "null"], "description": "Declared concurrency cap (fanout only); null/absent when unbounded." },
        "items": { "type": "string", "description": "Where the repetition count comes from: a run-time list source, a literal item count, or a declared iteration bound." },
        "literal_items": { "type": "integer", "description": "Statically known item count (fanout), or the declared max_iterations (loop)." },
        "run_time_list": { "type": "boolean", "description": "True when the count is only known at run time — the bound-honesty flag for cost." }
      }
    },
    "cost": {
      "type": "object",
      "additionalProperties": true,
      "required": ["dispatches", "dispatch_bound", "turns_priced", "wall_clock_priced", "exceeds_ceiling", "budget_checked"],
      "properties": {
        "dispatches": { "type": "integer", "description": "Worst-case dispatch count." },
        "dispatch_bound": {
          "enum": ["at most", "at least"],
          "description": "Honesty of the dispatch count: 'at most' when every repetition list is statically bounded, 'at least' when some list is only known at run time (unbound names them)."
        },
        "unbound": { "type": "array", "items": { "type": "string" }, "description": "Nodes whose repetition could not be statically bounded." },
        "turns": { "type": "integer", "description": "Worst-case turn total (0 when not priced)." },
        "turns_priced": { "type": "boolean" },
        "wall_clock_seconds": { "type": "integer" },
        "wall_clock_priced": { "type": "boolean" },
        "ceiling": { "type": "integer", "description": "Effective ceiling used for the budget check (the document's declaration, else the host default)." },
        "exceeds_ceiling": { "type": "boolean", "description": "True when worst-case dispatches exceed the effective ceiling (WF034)." },
        "declared_ceiling": { "type": ["integer", "null"], "description": "The document's own ceiling, when declared." },
        "budget_checked": { "type": "boolean" }
      }
    },
    "proposal": {
      "type": "object",
      "additionalProperties": true,
      "required": ["proposal", "dispositions", "actions", "once_on_every_path", "settled_on_every_path", "stranded"],
      "properties": {
        "proposal": { "type": "string", "description": "Canonical name of the proposal-producing boundary (node path or bound name)." },
        "dispositions": {
          "type": "array",
          "items": { "type": "string" },
          "description": "Settlement actions the proof observed for this producer, as node paths or verb records. Never null: [] means the proof observed no disposition — an undisposed token (the WF026 leak case)."
        },
        "actions": { "type": "string", "description": "Settlement verbs the proof observed." },
        "once_on_every_path": { "type": "boolean", "description": "Settled at most/exactly once on every path (WF021/WF022 territory)." },
        "settled_on_every_path": { "type": "boolean", "description": "Every path through the graph settles it (no leak)." },
        "stranded": { "type": "boolean", "description": "Can reach exit unsettled (WF026)." }
      }
    }
  }
}
`
