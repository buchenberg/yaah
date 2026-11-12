package rolefile

// SchemaJSON is the published JSON Schema for a sub-agent role file's
// YAML frontmatter (yaah-21uk D2, the first half of the portability
// item: the frontmatter gains a machine-readable description second
// implementations and editors can validate against, so a naah-style
// port cannot silently drop a field it never knew existed).
//
// Drift discipline: schema_test.go pins three things — the schema parses
// as JSON, its property set is exactly the Frontmatter struct's fields
// (reflection over the same yaml tags Parse reads), and the copy under
// docs/schemas/rolefile-frontmatter.schema.json is byte-identical to
// this constant, so the published artifact cannot fall behind the
// parser. Order inside the mapping objects is not expressible in JSON —
// and IS policy in the role vocabulary (last-match-wins), so permissions
// keeps its order note in the property description.
const SchemaJSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://yaah.dev/schemas/rolefile-frontmatter.schema.json",
  "title": "yaah role file frontmatter",
  "description": "YAML frontmatter of a yaah sub-agent role file (markdown body follows the closing ---). Parsed by internal/rolefile; the registry normalizes mode on load. Mapping order is policy for permissions (last-match-wins), which JSON objects do not preserve — editors should treat reordering as a content change.",
  "type": "object",
  "additionalProperties": true,
  "properties": {
    "name": {
      "type": "string",
      "description": "Role name; dispatch surfaces (list_subagents, delegate) list it."
    },
    "description": {
      "type": "string",
      "description": "One-line description of what the role does."
    },
    "specialty": {
      "type": "string",
      "description": "The narrow specialty the orchestrator delegates for."
    },
    "contract": {
      "type": "object",
      "description": "Structured output block the sub-agent must append to its response.",
      "additionalProperties": false,
      "properties": {
        "heading": {
          "type": "string",
          "description": "Markdown heading the block appears under."
        },
        "fields": {
          "type": "array",
          "description": "Response contract fields; a bare string is a field name.",
          "items": {
            "oneOf": [
              { "type": "string" },
              {
                "type": "object",
                "additionalProperties": false,
                "properties": {
                  "name": { "type": "string" },
                  "kind": {
                    "type": "string",
                    "enum": ["evidence", "interpretation"],
                    "description": "evidence = verifiable raw tool output; empty or interpretation = model synthesis."
                  }
                },
                "required": ["name"]
              }
            ]
          }
        }
      }
    },
    "tools": {
      "type": "array",
      "description": "The role's tool belt; the dispatch registry is built from exactly this list.",
      "items": { "type": "string" }
    },
    "output": {
      "type": "string",
      "enum": ["report", "proposal", ""],
      "description": "Dispatch output contract: report returns text (isolated-report or shared lane), proposal seals an isolated worktree delta for the orchestrator to settle. Empty means proposal."
    },
    "mode": {
      "type": "string",
      "enum": ["primary", "subagent", "all", ""],
      "description": "Agent capability axis: primary is orchestrator-eligible, subagent is delegate-only, all is both. Empty means all; an unknown value fails closed to subagent at load."
    },
    "max_iterations": {
      "type": "integer",
      "minimum": 0,
      "description": "Budget ceiling on loop cycles; 0 leaves the configured default."
    },
    "min_iterations": {
      "type": "integer",
      "minimum": 0,
      "description": "Budget floor on loop cycles; 0 = none."
    },
    "max_turns": {
      "type": "integer",
      "minimum": 0,
      "description": "Budget ceiling on tool turns; 0 leaves the configured default."
    },
    "min_turns": {
      "type": "integer",
      "minimum": 0,
      "description": "Budget floor on tool turns; 0 = none."
    },
    "json_mode": {
      "type": "boolean",
      "description": "Request JSON-mode responses from the provider."
    },
    "timeout": {
      "type": "integer",
      "minimum": 0,
      "description": "Dispatch timeout in seconds; 0 = no timeout."
    },
    "system": {
      "type": "string",
      "enum": ["replace", ""],
      "description": "How the body enters the session system prompt: empty layers it after the embedded identity; replace swaps it in for the identity (environment, project instructions, memory, skills, and session directives still layer after). Anything but replace warns at load."
    },
    "provider": {
      "type": "string",
      "description": "Per-role provider routing; empty falls back to the configured default chain."
    },
    "model": {
      "type": "string",
      "description": "Per-role model routing; empty falls back to the configured default chain."
    },
    "permissions": {
      "type": "object",
      "description": "The role's permission ruleset: tool name to either a bare effect (every call of that tool) or a mapping of resource glob to effect. Rules keep source order and the last matching rule wins; deny refines within the tool belt (never re-enables a withheld tool), ask routes through the session's approval pipeline (and a headless dispatch denies it). JSON objects do not preserve mapping order — which is policy here — so validate order outside JSON tooling.",
      "additionalProperties": {
        "oneOf": [
          { "$ref": "#/$defs/effect" },
          {
            "type": "object",
            "additionalProperties": { "$ref": "#/$defs/effect" }
          }
        ]
      }
    },
    "headless": {
      "type": "string",
      "enum": ["deny", ""],
      "description": "Declares what a headless run does with this role's ask rules: a sub-agent dispatch denies them (nobody is attached to prompt). Declaring it silences the load-time warning a subagent-capable role with ask rules raises. Empty is the historical default."
    },
    "temperature": {
      "type": "number",
      "minimum": 0,
      "maximum": 2,
      "description": "Per-role sampling override passed through to the provider request."
    },
    "top_p": {
      "type": "number",
      "minimum": 0,
      "maximum": 1,
      "description": "Per-role sampling override passed through to the provider request."
    }
  },
  "$defs": {
    "effect": {
      "type": "string",
      "enum": ["allow", "deny", "ask"],
      "description": "What a matching rule does to a tool call: allow proceeds (still subject to the mode-axis floor), deny strips the call, ask routes through the approval pipeline."
    }
  }
}
`
