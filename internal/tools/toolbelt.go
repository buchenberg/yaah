package tools

// toolbelt.go is the lazy toolbelt's loader (yaah-6mi9.5, designed in
// docs/lazy-toolbelt-design.md). The curated belt folds rarely-called
// families to stub lines in the prompt; this tool is the other half:
// `action: mount` registers a family for the rest of the session, paying
// the schema tariff once, per member, through the same admission the
// startup path uses. It mirrors the sub-agent UX (list_subagents is the
// cheap advertisement, delegate the instantiation): `action: list` is
// the cheap advertisement, mount the instantiation.
//
// The mount flows through the existing stages; none is bypassed:
//
//   - Registry.Admit(name) + Register per member — the MCP seam
//     (wiring_mcp.go), which widens the belt and retracts the folded
//     name's startup refusal. A mounted tool is admitted through the
//     belt, never alongside it.
//   - Admits (the session's nameAdmission answer, wired by cmd/yaah)
//     runs at mount time so a persona list or a mode floor that would
//     withhold every member fails the mount citing the refusing
//     origin, instead of "succeeding" into a family the model can never
//     call. Per-call floors still evaluate each mounted call — a mount
//     adds schemas, never permission.
//   - The per-loop persona narrowing (internal/agent
//     narrowToPersonaTools) is unchanged: a mounted name still narrows
//     away under a persona that excludes it.
//
// Sub-agents do not get this tool (docs/lazy-toolbelt-design.md,
// decision 4): their surface is their role profile, and the registry
// here is the session orchestrator's.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	kernel "github.com/buchenberg/goatherd-kernel"
	"github.com/buchenberg/yaah/internal/prompts"
)

// ToolFamily is one mountable family: a name, a one-line purpose for
// the stub block, and full membership — every member is named, no
// ellipses, because the stub is the recognition surface and a hidden
// member is a tool the model cannot ask for (yaah-6mi9.4 review:
// `toolbelt action: list` exists as the authoritative membership
// surface, but recognition must not depend on a second call).
type ToolFamily struct {
	Name        string
	Description string
	Tools       []string
}

// toolFamilies is the first gate set (yaah-6mi9.5): the withheld file
// cluster as the restore path, and the two always-on folds. The wake
// pair is deliberately absent — it is session-shape gated
// (yaah-6mi9.2), not mountable; a mount must not open a gate.
// Membership is pinned against the leaf factory (leafTools) and
// removedFromOrchestratorDefault by orchestrator_belt_test.go.
var toolFamilies = []ToolFamily{
	{
		Name:        "file_ops",
		Description: "one-shot file moves/copies/listing",
		Tools: []string{
			"append", "bulk_files", "checksum", "copy", "copy_tree",
			"find", "mkdir", "move", "move_tree", "rmdir", "tail",
		},
	},
	{
		Name:        "analysis",
		Description: "query and compare workspace content",
		Tools:       []string{"calculate", "json_query", "file_info", "diff", "bisect"},
	},
	{
		Name:        "net",
		Description: "direct HTTP requests and web page fetches",
		Tools:       []string{"http", "webfetch"},
	},
}

// ToolFamilies returns the mountable families in declaration order.
// The declaration order is the stub block's order and part of its
// cost model: the block rides the prompt on every call.
func ToolFamilies() []ToolFamily {
	out := make([]ToolFamily, len(toolFamilies))
	copy(out, toolFamilies)
	return out
}

// FamilyOf returns the family a tool name belongs to ("" and false if
// none). The registry's unknown-tool error path uses it to answer a
// direct call to a folded-but-unmounted tool instructively (see
// Registry.Execute) — the stub table created the recognition, and the
// error path must not waste it.
func FamilyOf(name string) (string, bool) {
	for _, f := range toolFamilies {
		if slices.Contains(f.Tools, name) {
			return f.Name, true
		}
	}
	return "", false
}

// MountFactAppender is the fact seam the session root satisfies. Named
// here to keep this package off the runtime root, exactly like
// apply_out's EffectSink names its own appender; *goatherd.Scope
// satisfies it with the method it already exports.
type MountFactAppender interface {
	AppendFact(ctx context.Context, schemaRef, kindLabel string, payload map[string]any) (kernel.AppendReceipt, error)
}

// MountSink records a mount as a yaah.tool.mounted.v1 fact on the
// session's root scope, attributed to OpID — the same owner-path
// attribution the accept seam uses (wiring_stages.go). A nil Appender
// makes recording a silent no-op: without a root scope there is no
// owner path to append to, and a tracing failure must not fail the
// mount.
type MountSink struct {
	Appender MountFactAppender
	OpID     string
}

// ToolbeltTool lists and mounts the toolbelt's families. Registered
// beside the orchestration spine only; the belt must admit "toolbelt"
// or the session keeps its startup shape (no stub block — see
// wiring_assemble.go).
type ToolbeltTool struct {
	// Reg is the session's registry. Mount registers through it, so
	// the belt (its construction constraint) stays the single
	// enforcement point.
	Reg *Registry

	// Admits is the session's mount-time nameAdmission answer:
	// allowed, and the snake_case origin vocabulary for a refusal
	// (persona, mode_floor). Nil admits everything — the
	// registry/belt enforcement alone. Advertise-time and per-call
	// floors are NOT this callback's to enforce; it exists so a
	// family that could not surface a single member fails the mount
	// up front instead of mounting dead schemas.
	Admits func(name string) (allowed bool, origin string)

	// sink is late-bound (SetSink) once the session's root scope
	// exists, like the accept seam's effect sink: at registration
	// time the scope does not exist yet.
	sink *MountSink
}

// SetSink late-binds the fact sink. Called from the wiring stage that
// creates the root scope; before it (or never, in a session without
// one) recording is a silent no-op.
func (t *ToolbeltTool) SetSink(s *MountSink) { t.sink = s }

func (t *ToolbeltTool) Name() string { return "toolbelt" }

func (t *ToolbeltTool) Description() string {
	return prompts.ToolDescription("toolbelt")
}

func (t *ToolbeltTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {"type": "string", "enum": ["list", "mount"], "description": "Action to perform (default: list)"},
			"family": {"type": "string", "description": "Family name (required for mount)"}
		}
	}`)
}

// IsDangerous reports false always: mounting registers schemas, and
// every mounted call still passes its own per-call gates and approval
// exactly as a startup-registered call would. A mount adds a schema,
// never a permission.
func (t *ToolbeltTool) IsDangerous(argsJSON string) bool { return false }

// toolbeltArgs is the strict-args shape of the toolbelt schema, and
// the payload keys of the mounted fact.
type toolbeltArgs struct {
	Action string `json:"action"`
	Family string `json:"family"`
}

func (t *ToolbeltTool) Execute(ctx context.Context, args string) (string, error) {
	var p toolbeltArgs
	if err := StrictToolArgs(t.Name(), t.Schema(), args, &p); err != nil {
		return "", err
	}
	switch p.Action {
	case "", "list":
		return t.list(), nil
	case "mount":
		return t.mount(ctx, p.Family)
	default:
		return "", fmt.Errorf("%s: unknown action %q (use list or mount)", t.Name(), p.Action)
	}
}

// list renders the family table with each family's live state, so a
// model that lost the stub block (compaction) can still find the
// mountable surface.
func (t *ToolbeltTool) list() string {
	var b strings.Builder
	b.WriteString("Mountable tool families (mount is sticky for the session; already-mounted members stay mounted):\n")
	for _, f := range toolFamilies {
		state := "not mounted"
		if t.Reg != nil && familyMounted(t.Reg, f) {
			state = "mounted"
		}
		fmt.Fprintf(&b, "- %s (%s) — %s: %s\n", f.Name, f.Description, state, strings.Join(f.Tools, ", "))
	}
	return b.String()
}

// familyMounted reports whether every member of a family registers —
// the "mounted" state the list action reports. Partially mounted
// families still read not mounted; the missing members can mount.
func familyMounted(reg *Registry, f ToolFamily) bool {
	for _, name := range f.Tools {
		if reg.Get(name) == nil {
			return false
		}
	}
	return true
}

// mount registers a family's members through the belt and the
// session's admission answer. The family mount is atomic against the
// per-member result: if every member is refused, nothing registers and
// the belt admissions revert, so the mount's belt diff is empty and
// the failed mount answer is that turn's whole cost — no prefix
// re-warm, no schemas the model can never call (decision 3 in
// docs/lazy-toolbelt-design.md). A success always means at least one
// member is callable now.
func (t *ToolbeltTool) mount(ctx context.Context, family string) (string, error) {
	if t.Reg == nil {
		return "", fmt.Errorf("%s: no tool registry in this session", t.Name())
	}
	var fam *ToolFamily
	for i := range toolFamilies {
		if toolFamilies[i].Name == family {
			fam = &toolFamilies[i]
			break
		}
	}
	if fam == nil {
		names := make([]string, len(toolFamilies))
		for i, f := range toolFamilies {
			names[i] = f.Name
		}
		return "", fmt.Errorf("%s: unknown family %q (known families: %s)", t.Name(), family, strings.Join(names, ", "))
	}

	var (
		registered []string          // mounted by this call
		already    []string          // were already registered
		refused    map[string]string // name -> origin
		admitted   []string          // belt widenings to revert on atomic failure
	)
	refused = make(map[string]string)
	for _, name := range fam.Tools {
		if t.Reg.Get(name) != nil {
			already = append(already, name)
			continue
		}
		if NewLeafTool(name) == nil {
			refused[name] = "unknown"
			continue
		}
		if t.Admits != nil {
			if ok, origin := t.Admits(name); !ok {
				refused[name] = origin
				continue
			}
		}
		t.Reg.Admit(name)
		admitted = append(admitted, name)
		t.Reg.Register(NewLeafTool(name))
		registered = append(registered, name)
	}

	if len(registered) == 0 && len(already) == 0 {
		// Atomic failure: revert the belt admissions so the belt's
		// refusal set is exactly what it was — the read-only or
		// persona-refused family never widened. Withdraw re-refuses
		// the name; the folded startup state is thereby restored.
		for _, name := range admitted {
			t.Reg.Withdraw(name)
		}
		details := make([]string, 0, len(refused))
		for name, origin := range refused {
			details = append(details, fmt.Sprintf("%s (%s)", name, origin))
		}
		sort.Strings(details)
		return "", fmt.Errorf("%s: mount refused — no member of the %s family is callable in this session: %s",
			t.Name(), fam.Name, strings.Join(details, ", "))
	}

	t.recordMount(ctx, fam.Name, registered, refused)

	var b strings.Builder
	if len(registered) == 0 {
		fmt.Fprintf(&b, "%s family already fully mounted: %s\n", fam.Name, strings.Join(already, ", "))
		return b.String(), nil
	}
	fmt.Fprintf(&b, "mounted %s family: %s\n", fam.Name, strings.Join(registered, ", "))
	if len(already) > 0 {
		fmt.Fprintf(&b, "already mounted: %s\n", strings.Join(already, ", "))
	}
	if len(refused) > 0 {
		details := make([]string, 0, len(refused))
		for name, origin := range refused {
			details = append(details, fmt.Sprintf("%s (%s)", name, origin))
		}
		sort.Strings(details)
		fmt.Fprintf(&b, "not mounted (withheld by the session): %s\n", strings.Join(details, ", "))
	}
	return b.String(), nil
}

// recordMount appends the yaah.tool.mounted.v1 fact on the session's
// root scope. Best-effort, like the confinement fact: a tracing
// failure must not fail the mount — the schemas registered either
// way, and the per-turn session.context records already carry the
// effective belt, so the diff between consecutive records still shows
// the mount even without this fact. Only a real mount records: an
// idempotent remount appends nothing, the same way it registers
// nothing (sticky per session, decision in the design doc).
func (t *ToolbeltTool) recordMount(ctx context.Context, family string, registered []string, refused map[string]string) {
	if t.sink == nil || t.sink.Appender == nil || len(registered) == 0 {
		return
	}
	payload := map[string]any{
		"family": family,
		"tools":  registered,
	}
	if len(refused) > 0 {
		payload["refused"] = refused
	}
	if _, err := t.sink.Appender.AppendFact(ctx, "yaah.tool.mounted.v1", "tool", payload); err != nil {
		// Deliberately swallowed: see the method comment.
		_ = err
	}
}
