package yaah

import (
	"fmt"

	"github.com/spf13/cobra"
)

// workflow.go is the CLI skeleton for the workflow DSL's static analysis
// (bead yaah-0u0v.9): `yaah workflow validate` and `yaah workflow explain`.
//
// Both commands are read-only by construction. They read one file, decode
// it, resolve it into the workflow graph, run the validator families over
// that graph, and print what the families said. No agent is dispatched, no
// session or store is opened, no worktree is created, and no run state is
// written. The cmd layer owns flags, file loading, formatting, and the exit
// code; every judgement lives in internal/workflow/....
//
// The rule codes and what each one means are documented in
// internal/workflow/validate/doc.go, which is the contract between the
// validator families and the page this help text renders.

const workflowExitHelp = `Exit codes:
  0  no findings at error severity
  1  findings at error severity; any finding at all with --strict; or the
     analysis could not run at all (missing or unreadable file, a spec over
     the decoder's size limit, a malformed --param). With --json the report
     still goes to stdout and the summary line to stderr, and "status"
     separates "unresolved" (the document never became a graph, so the
     node-level families could not run) from "invalid". "status" is the
     document's verdict: --strict flips "ok" and the exit code but never
     rewrites "status", so status=invalid always implies a non-empty
     "errors".`

const workflowSeverityHelp = `Severity:
  error    a defect in the document itself, true on every host
           (WF001-WF029, WF034, WF036)
  warning  a finding whose truth depends on this host's configuration —
           the role registry (WF030), the world registry (WF031), the
           network ceiling (WF032), the dispatch budget (WF033), the
           configured caps (WF035). WF030, WF031 and WF033 also carry
           document-intrinsic halves which stay warnings for whole-code
           consistency — the code is the contract. --strict promotes
           warnings to failures (ok and exit code, never status).`

func init() {
	rootCmd.AddCommand(newWorkflowCmd())
}

func newWorkflowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflow",
		Short: "Validate and explain workflow specs (static analysis only)",
		Long: `Inspect a workflow spec without running it.

  yaah workflow validate <spec>   check every rule family
  yaah workflow explain  <spec>   report the plan: graph, fan-out, cost,
                                  authority union, proposal dispositions
  yaah workflow run      <spec>   validate, then execute and report outputs

validate and explain are static analysis: neither dispatches an agent,
starts a session, nor writes run state. run executes — through the host
door and internal/dispatch, after the same validation gate.

The --json report shapes are stable within v1 and documented as JSON Schema:
  docs/schemas/workflow-validate.schema.json   (kind yaah.workflow.validate.v1)
  docs/schemas/workflow-explain.schema.json    (kind yaah.workflow.explain.v1)`,
	}
	cmd.AddCommand(newWorkflowValidateCmd())
	cmd.AddCommand(newWorkflowExplainCmd())
	cmd.AddCommand(newWorkflowRunCmd())
	return cmd
}

// workflowOptions is the flag set both subcommands share. It is per-command
// state captured by the constructor rather than a package global, because
// Execute() can be called more than once in a process (tests, embedding)
// and a shared target would carry flags from the previous run.
type workflowOptions struct {
	params []string
	strict bool
	asJSON bool
}

func (o *workflowOptions) bind(cmd *cobra.Command, explain bool) {
	cmd.Flags().StringArrayVar(&o.params, "param", nil,
		"set a declared workflow parameter (key=value, repeatable); list and map take JSON")
	if explain {
		cmd.Flags().BoolVar(&o.asJSON, "json", false,
			"emit the documented yaah.workflow.explain.v1 object instead of the text report")
	} else {
		cmd.Flags().BoolVar(&o.asJSON, "json", false,
			"emit the documented yaah.workflow.validate.v1 object instead of the text report")
	}
	cmd.Flags().BoolVar(&o.strict, "strict", false,
		"treat host-relative warnings as failures")
}

func newWorkflowValidateCmd() *cobra.Command {
	var opts workflowOptions
	cmd := &cobra.Command{
		Use:   "validate <spec.yaml|spec.json>",
		Short: "Check a workflow spec against every validator family",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflow(cmd, args[0], opts, false)
		},
		Long: fmt.Sprintf(`Decode the document, resolve it into the workflow graph, and run
every validator family over it:

  structural  WF001-WF005   is this a graph at all
  values      WF010-WF014   does every value it moves resolve, in scope,
                            in the shape its producer declares
  linearity   WF020-WF029   is every proposal settled exactly once
  authority   WF030-WF036   what may this run touch, against this host
  cost        WF033-WF035   how much may it spend, against this host

Findings print as code, path, message — one per line, ordered by code then
path so two runs over one document agree — and the whole set is reported at
once rather than stopping at the first.

%s

%s

--json shape (yaah.workflow.validate.v1):
  {
    "kind": "yaah.workflow.validate.v1",
    "spec": "path/as/given.yaml",
    "name": "metadata.name",
    "status": "valid" | "invalid" | "unresolved",
    "ok": true,
    "strict": false,
    "errors":   [ { "code": "WF002", "path": "steps.a.needs", "message": "..." } ],
    "warnings": [ ]
  }
errors and warnings are always arrays, never null; findings are ordered by
code, then path, then message.`, workflowSeverityHelp, workflowExitHelp),
	}
	opts.bind(cmd, false)
	return cmd
}

func newWorkflowExplainCmd() *cobra.Command {
	var opts workflowOptions
	cmd := &cobra.Command{
		Use:   "explain <spec.yaml|spec.json>",
		Short: "Report a workflow spec's graph, fan-out, cost, authority, and dispositions",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorkflow(cmd, args[0], opts, true)
		},
		Long: fmt.Sprintf(`Print the plan a workflow document states, from the same data the
validators compute — explain adds no analysis of its own:

  nodes       every resolved node with its role, lane, world, network,
              grants, and per-kind bounds; a value inherited from host
              configuration is marked with *
  dag         the resolved graph as Mermaid (needs, branch arms, gate
              dispositions, and containment as subgraphs)
  fan-out     the maximum declared concurrency and every container that
              states it
  cost        worst-case dispatch count with its bound honesty flag
              (at most / at least), the turn and wall-clock bound when this
              host prices them, and the effective dispatch ceiling
  authority   the union of every reachable grant, artifact path, and
              network mode, and the broker hosts configured behind a
              broker mode
  proposals   every proposal-producing node and its guaranteed disposition,
              from the linearity proof
  findings    the same code/path/message list validate prints

An empty settle policy is displayed as the runtime default path_disjoint and
marked (default), because resolution does not materialize it. The guaranteed
disposition of a proposal comes from the settlement proof, not from an
on_abort field: the v1 vocabulary has none.

%s

%s

--json shape (yaah.workflow.explain.v1): the same report as one object, with
"host", "params", "graph", "nodes", "edges", "mermaid", "fanout", "cost",
"authority", "settlement", "errors", and "warnings". Every list is sorted
and emitted as [] rather than null, and HTML escaping is off, so one
document always encodes to the same bytes.`, workflowSeverityHelp, workflowExitHelp),
	}
	opts.bind(cmd, true)
	return cmd
}

// runWorkflow is the whole command: resolve the host authority, analyse the
// document, print it, and map the verdict onto an exit code.
func runWorkflow(cmd *cobra.Command, path string, opts workflowOptions, explain bool) error {
	a, err := analyzeWorkflow(path, opts.params, workflowEnv(), opts.strict)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if opts.asJSON {
		if err := writeWorkflowJSON(out, a, explain); err != nil {
			return err
		}
	} else if explain {
		renderWorkflowExplain(out, a)
	} else {
		renderWorkflowValidate(out, a)
	}
	return workflowExitError(a)
}

// workflowExitError turns the verdict into the process exit code. The
// report has already been written, so this error is the summary line the
// shell sees, not a second copy of the findings.
func workflowExitError(a *workflowAnalysis) error {
	if !a.failed() {
		return nil
	}
	if len(a.errors) > 0 {
		return fmt.Errorf("%s: %d error(s), %d warning(s) — %s", a.path, len(a.errors), len(a.warnings), a.status())
	}
	return fmt.Errorf("%s: %d warning(s) treated as errors (--strict)", a.path, len(a.warnings))
}
