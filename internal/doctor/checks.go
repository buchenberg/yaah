package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	shepherdtrace "github.com/buchenberg/goatherd-kernel"

	"github.com/buchenberg/goatherd-runtime/sandbox/confinement"
	"github.com/buchenberg/yaah/internal/agent/pipeline"
	"github.com/buchenberg/yaah/internal/agent/recovery"
	"github.com/buchenberg/yaah/internal/config"
	"github.com/buchenberg/yaah/internal/jail"
)

// Check represents a single diagnostic result.
type Check struct {
	Label  string
	Detail string
	Status string // "OK", "WARN", "FAIL"
}

// Options holds configuration for RunChecks. Provides explicit state
// that was previously passed via package-level globals.
type Options struct {
	DirectiveOverrides []string
}

// doctorUseColor is set based on NO_COLOR environment variable.
var doctorUseColor = os.Getenv("NO_COLOR") == ""

// RunChecks executes all diagnostic checks and returns the results.
func RunChecks(opts Options) []Check {
	cfg, cfgErr := config.Load()
	cfgPath, pathErr := config.ConfigPath()

	return []Check{
		CheckConfigFile(cfgPath, cfg, cfgErr, pathErr),
		CheckOldConfig(cfgPath),
		CheckProvider(cfg, cfgErr),
		CheckModel(cfg, cfgErr),
		CheckSubAgents(cfg, cfgErr),
		CheckContainment(cfg, cfgErr),
		CheckWorkspace(cfg, cfgErr),
		CheckFallback(cfg, cfgErr),
		CheckQualityGates(cfg, cfgErr),
		CheckDirectives(cfg, cfgErr, opts.DirectiveOverrides),
		CheckPipeline(cfg, cfgErr),
		CheckShepherd(cfg, cfgErr),
		CheckOTel(cfg, cfgErr),
		CheckHomeWritable(),
		CheckPlatform(),
		CheckEditor(cfg),
	}
}

// AllOK returns true if all checks have status "OK".
func AllOK(checks []Check) bool {
	for _, c := range checks {
		if c.Status != "OK" {
			return false
		}
	}
	return true
}

// CheckConfigFile checks the configuration file existence and validity.
func CheckConfigFile(path string, cfg *config.Config, cfgErr, pathErr error) Check {
	if pathErr != nil {
		return Check{Label: "Config path", Status: "FAIL", Detail: pathErr.Error()}
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return Check{
			Label:  "Config file",
			Detail: fmt.Sprintf("not found at %s — using built-in defaults", path),
			Status: "WARN",
		}
	}
	if cfgErr != nil {
		return Check{Label: "Config file", Status: "FAIL", Detail: cfgErr.Error()}
	}
	detail := fmt.Sprintf("%s (model: %s, %d provider(s))", path, cfg.Agent.Default.Model, len(cfg.Providers))
	return Check{Label: "Config file", Status: "OK", Detail: detail}
}

// CheckOldConfig checks if the config file uses the old format.
func CheckOldConfig(path string) Check {
	if path == "" {
		return Check{Label: "Config format", Status: "OK", Detail: "no config file"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Check{Label: "Config format", Status: "WARN", Detail: fmt.Sprintf("cannot read: %v", err)}
	}
	if config.HasOldConfig(data) {
		return Check{
			Label:  "Config format",
			Status: "WARN",
			Detail: "old-style config detected — rename 'default:' to 'agents: default:' and 'agent:' to 'agents:'",
		}
	}
	return Check{Label: "Config format", Status: "OK", Detail: "agents.* format"}
}

// CheckProvider checks the provider configuration.
func CheckProvider(cfg *config.Config, cfgErr error) Check {
	if cfgErr != nil {
		return Check{Label: "Providers", Status: "WARN", Detail: "config not loaded"}
	}
	if len(cfg.Providers) == 0 {
		return Check{Label: "Providers", Status: "FAIL", Detail: "no providers configured — add at least one provider in config.yaml"}
	}

	providerName := cfg.Agent.Default.Provider
	if providerName == "" {
		providerName = resolveProviderName(cfg)
	}

	if _, ok := cfg.Providers[providerName]; !ok {
		return Check{
			Label:  "Default provider",
			Status: "WARN",
			Detail: fmt.Sprintf("no provider key %q found — check default.provider or set a provider prefix on default.model", providerName),
		}
	}

	var issues []string
	for name, p := range cfg.Providers {
		if p.APIKey == "" || p.APIKey == "ollama" {
			continue
		}
		if strings.HasPrefix(p.APIKey, "${") && strings.HasSuffix(p.APIKey, "}") {
			envVar := p.APIKey[2 : len(p.APIKey)-1]
			if os.Getenv(envVar) == "" {
				issues = append(issues, fmt.Sprintf("%s: env var %s is empty or unset", name, envVar))
			}
		}
	}

	if len(issues) > 0 {
		return Check{
			Label:  "Provider API keys",
			Status: "WARN",
			Detail: strings.Join(issues, "; "),
		}
	}

	return Check{Label: "Providers", Status: "OK", Detail: fmt.Sprintf("%d configured, default is %q", len(cfg.Providers), providerName)}
}

// CheckModel checks the default model configuration.
func CheckModel(cfg *config.Config, cfgErr error) Check {
	if cfgErr != nil {
		return Check{Label: "Default model", Status: "WARN", Detail: "config not loaded"}
	}
	if cfg.Agent.Default.Model == "" {
		return Check{Label: "Default model", Status: "FAIL", Detail: "default.model is not set"}
	}
	return Check{Label: "Default model", Status: "OK", Detail: cfg.Agent.Default.Model}
}

// CheckSubAgents reports the sub-agent provider/model configuration.
func CheckSubAgents(cfg *config.Config, cfgErr error) Check {
	if cfgErr != nil {
		return Check{Label: "Sub-agents", Status: "WARN", Detail: "config not loaded"}
	}
	sc := cfg.Agent.SubAgent
	providerName := sc.Provider
	model := sc.Model
	if model == "" {
		model = cfg.Agent.Default.Model
	}
	if providerName == "" {
		providerName = resolveProviderName(cfg)
		return Check{
			Label:  "Sub-agents",
			Status: "OK",
			Detail: fmt.Sprintf("enabled — inherit planner model (%s / %s, max depth 1, max concurrency %d)", providerName, model, sc.MaxConcurrency),
		}
	}
	if _, ok := cfg.Providers[providerName]; !ok {
		return Check{
			Label:  "Sub-agents",
			Status: "WARN",
			Detail: fmt.Sprintf("provider %q not found for sub-agents — falling back to planner (%s / %s)", sc.Provider, providerName, model),
		}
	}
	return Check{
		Label:  "Sub-agents",
		Status: "OK",
		Detail: fmt.Sprintf("enabled — %s / %s, max depth 1, max concurrency %d", providerName, model, sc.MaxConcurrency),
	}
}

// CheckContainment reports the sub-agent syscall-jail capability (Plan 7 Phase
// 3): whether a containment backend is available here (Landlock on Linux,
// winacl on Windows), and how agents.subagent's
// mode + containment will use it. A `paddock` placement on a host with no
// backend fails closed, so it is surfaced as a FAIL rather than discovered at
// dispatch.
func CheckContainment(cfg *config.Config, cfgErr error) Check {
	if cfgErr != nil {
		return Check{Label: "Containment", Status: "WARN", Detail: "config not loaded"}
	}
	sc := cfg.Agent.SubAgent
	backend := jail.Backend()
	available := false
	backendDesc := "none (no jail on this host)"
	if backend != nil {
		if ok, why := backend.Available(); ok {
			available = true
			if why != "" {
				backendDesc = why
			} else {
				backendDesc = "available"
			}
		} else if why != "" {
			backendDesc = "unusable — " + why
		} else {
			backendDesc = "unusable"
		}
	}
	// Network rules are a separate capability from the jail itself: Landlock ABI
	// 1-3 confines the filesystem and nothing else, so a restrictive egress
	// posture is a FAIL there rather than a claim of enforcement.
	netMode := confinement.NetMode(strings.ToLower(strings.TrimSpace(sc.Network)))
	if netMode == "" {
		netMode = confinement.NetAllowAll
	}
	netCapable := backend != nil && backend.SupportsNetwork(netMode)

	return containmentCheck(available, backendDesc, sc.Mode, sc.Containment, sc.Network, len(sc.NetworkHosts), netCapable)
}

// containmentCheck is the pure decision behind CheckContainment, split out so
// the present-but-unusable backend case is testable (the jail selection gates
// every platform on Available, so Detect never returns one, but a future
// backend or a race could).
func containmentCheck(available bool, backendDesc, mode, containment, network string, hosts int, networkCapable bool) Check {
	m := strings.ToLower(strings.TrimSpace(mode))
	if m == "" {
		m = "retained"
	}
	p := strings.ToLower(strings.TrimSpace(containment))
	if p == "" {
		// The config default: "auto" (Plan 7 Phase 3). Doctor reports the real
		// default, not a pre-flip "off".
		p = "auto"
	}
	n := strings.ToLower(strings.TrimSpace(network))
	if n == "" {
		n = "allow_all"
	}
	egress := "egress " + n
	if n == "broker" {
		egress += fmt.Sprintf(" (%d allowed host(s))", hosts)
	}
	detail := fmt.Sprintf("mode %s, placement %s, %s, backend %s", m, p, egress, backendDesc)
	if jailed(p, available) && n != "allow_all" {
		// The egress rule lives in the jail: without one there is nowhere to
		// put it, and the posture is a setting rather than a control.
		detail += " — enforced by the jail"
	}
	if n != "allow_all" && !networkCapable {
		// The dispatch refuses (a policy that cannot be applied is not applied
		// quietly), so this is not a warning about a weak control: sub-agent
		// dispatches will not run at all. Split the cause: a host without a
		// usable backend names that, not a feature some backend lacks.
		cause := "no containment backend on this host, so nothing enforces network rules"
		if available {
			cause = "this host's containment jail cannot enforce network rules (no network-rule support on this backend: Landlock needs ABI >= 4 on Linux, winacl has none)"
		}
		return Check{Label: "Containment", Status: "FAIL",
			Detail: detail + " — " + cause + ", and a restrictive egress posture is refused rather than run open; set network: allow_all or run where it can be enforced"}
	}

	switch p {
	case "paddock", "jail":
		if !available {
			return Check{Label: "Containment", Status: "FAIL", Detail: detail + " — placement=paddock fails closed with no usable backend; every dispatch would refuse"}
		}
		return Check{Label: "Containment", Status: "OK", Detail: detail + " — sub-agents are jailed"}
	case "auto":
		if available {
			return Check{Label: "Containment", Status: "OK", Detail: detail + " — sub-agents are jailed"}
		}
		return Check{Label: "Containment", Status: "WARN", Detail: detail + " — auto degrades visibly to advisory here"}
	default: // off / none / advisory
		// A restrictive egress posture with no jail to enforce it is refused at
		// dispatch, not quietly dropped — so this is a FAIL, not a warning about
		// a weak control: sub-agent dispatches will not run at all.
		if n != "allow_all" {
			return Check{Label: "Containment", Status: "FAIL",
				Detail: detail + " — sub-agents run unconfined on the host and a restrictive egress posture is refused rather than run open; set containment: auto (or paddock) to enforce it, or network: allow_all"}
		}
		return Check{Label: "Containment", Status: "OK", Detail: detail + " — sub-agents run unconfined on the host"}
	}
}

// jailed reports whether a placement puts sub-agents in a jail on this host.
func jailed(placement string, available bool) bool {
	switch placement {
	case "paddock", "jail":
		return available
	case "auto":
		return available
	default:
		return false
	}
}

// CheckWorkspace reports the workspace substrate the session will run on
// (the isolated-workspace plan §2.4): the configured backend, the sandbox
// image and containment root in isolated mode, and the backends this
// binary actually links. Until this check, whether isolation was active
// was discoverable only by reading a source comment.
//
// The check is deliberately config-level: the daemon and the image's D5
// contract are probed at session start (inside the startup bound), and a
// doctor run that booted throwaway containers would be a probe, not a
// diagnosis. The detail line names the libraries the binary really
// depends on — croft for the isolated workspace, shepherd for supervision —
// so an "unknown backend" failure is diagnosable here, against what the
// binary provides.
func CheckWorkspace(cfg *config.Config, cfgErr error) Check {
	if cfgErr != nil {
		return Check{Label: "Workspace", Status: "WARN", Detail: "config not loaded"}
	}
	w := cfg.Workspace
	switch {
	case w.Backend == "" || w.Backend == config.BackendLocal:
		return Check{Label: "Workspace", Status: "OK", Detail: "local — tools run on the host filesystem"}
	case w.Backend == config.BackendContainerd:
		if runtime.GOOS != "linux" {
			// Session wiring refuses this via config.Validate (config.Load
			// itself does not validate), so doctor is where a non-Linux
			// user learns it before the first session start.
			return Check{
				Label:  "Workspace",
				Status: "FAIL",
				Detail: fmt.Sprintf("isolated mode needs a Linux containerd daemon; %s cannot run one (native Linux or WSL2 required)", runtime.GOOS),
			}
		}
		// The same rules the session enforces at startup: config.Validate
		// runs at wiring, not load, so a stanza that would fail the
		// session (no image, a relative or whole-container root) must not
		// read as healthy here. The workspace-prefixed errors are the
		// stanza's own; the platform rule above is doctor's, reported
		// once.
		var wsErrs []string
		if verr := config.Validate(cfg); verr != nil {
			var ve config.ValidationError
			if errors.As(verr, &ve) {
				for _, e := range ve.Errors {
					if strings.HasPrefix(e, "workspace.") {
						wsErrs = append(wsErrs, e)
					}
				}
			}
		}
		if len(wsErrs) > 0 {
			return Check{Label: "Workspace", Status: "FAIL", Detail: strings.Join(wsErrs, "; ")}
		}
		detail := fmt.Sprintf("isolated (containerd) — image %s, namespace %s, root %s", w.Containerd.Image, w.Containerd.Namespace, w.Root)
		if w.Containerd.Address != "" {
			detail += fmt.Sprintf(", socket %s", w.Containerd.Address)
		} else {
			detail += ", socket containerd default"
		}
		detail += "; sandbox library: croft (containerd), supervision: shepherd (git); daemon and image contract probed at session start"
		return Check{Label: "Workspace", Status: "OK", Detail: detail}
	default:
		return Check{Label: "Workspace", Status: "FAIL", Detail: fmt.Sprintf("unknown backend %q (valid: local, containerd)", w.Backend)}
	}
}

// CheckFallback reports the fallback provider/model configuration.
func CheckFallback(cfg *config.Config, cfgErr error) Check {
	if cfgErr != nil {
		return Check{Label: "Fallback", Status: "WARN", Detail: "config not loaded"}
	}
	fc := cfg.Agent.Fallback
	if fc.Provider == "" {
		return Check{Label: "Fallback", Status: "OK", Detail: "not configured"}
	}
	if _, ok := cfg.Providers[fc.Provider]; !ok {
		return Check{
			Label:  "Fallback",
			Status: "WARN",
			Detail: fmt.Sprintf("provider %q not found for fallback (%s / %s)", fc.Provider, fc.Provider, fc.Model),
		}
	}
	return Check{
		Label:  "Fallback",
		Status: "OK",
		Detail: fmt.Sprintf("%s / %s", fc.Provider, fc.Model),
	}
}

// CheckQualityGates reports configured quality gate validators.
func CheckQualityGates(cfg *config.Config, cfgErr error) Check {
	if cfgErr != nil {
		return Check{Label: "Quality gates", Status: "WARN", Detail: "config not loaded"}
	}
	gates := cfg.Agent.QualityGates
	if len(gates) == 0 {
		return Check{Label: "Quality gates", Status: "OK", Detail: "not configured"}
	}
	var parts []string
	for role, validators := range gates {
		if len(validators) > 0 {
			parts = append(parts, fmt.Sprintf("%s→[%s]", role, strings.Join(validators, ",")))
		}
	}
	sort.Strings(parts)
	if len(parts) == 0 {
		return Check{Label: "Quality gates", Status: "OK", Detail: "configured but no active gates"}
	}
	return Check{Label: "Quality gates", Status: "OK", Detail: strings.Join(parts, ", ")}
}

// CheckDirectives reports active session directives from config.
func CheckDirectives(cfg *config.Config, cfgErr error, overrides []string) Check {
	if cfgErr != nil {
		return Check{Label: "Directives", Status: "WARN", Detail: "config not loaded"}
	}
	directives := cfg.Agent.Default.Directives
	cliCount := len(overrides)
	if len(directives) == 0 && cliCount == 0 {
		return Check{Label: "Directives", Status: "OK", Detail: "none configured"}
	}
	var detail string
	if cliCount > 0 && len(directives) > 0 {
		detail = fmt.Sprintf("%d from CLI, %d from config", cliCount, len(directives))
	} else if cliCount > 0 {
		detail = fmt.Sprintf("%d from CLI", cliCount)
	} else {
		detail = fmt.Sprintf("%d from config", len(directives))
	}
	return Check{Label: "Directives", Status: "OK", Detail: detail}
}

// CheckPipeline reports the active middleware pipeline. The default
// set is read from the pipeline package instead of maintaining a local
// copy that drifted out of sync (review B10e). middleware.enabled is
// additive over the defaults; disabled removes from the union. Names
// without a registered builder (e.g. deleted middleware still listed
// in config) are flagged — the pipeline silently skips them at build
// time, so reporting them as active would be dishonest.
func CheckPipeline(cfg *config.Config, cfgErr error) Check {
	if cfgErr != nil {
		return Check{Label: "Pipeline", Status: "WARN", Detail: "config not loaded"}
	}
	mc := cfg.Agent.Middleware
	active := pipeline.ResolvedPipelineNames(mc.Enabled, mc.Disabled)

	registered := make(map[string]bool)
	for _, name := range pipeline.RegisteredOrchestratorNames() {
		registered[name] = true
	}
	var unknown []string
	unknownSet := make(map[string]bool)
	for _, name := range active {
		if !registered[name] && !unknownSet[name] {
			unknownSet[name] = true
			unknown = append(unknown, name)
		}
	}

	// The honest active list only carries registered middleware.
	reported := make([]string, 0, len(active))
	for _, name := range active {
		if !unknownSet[name] {
			reported = append(reported, name)
		}
	}

	detail := fmt.Sprintf("%d middleware active: %s", len(reported), strings.Join(reported, " → "))
	if len(mc.Disabled) > 0 {
		detail += fmt.Sprintf(" (disabled: %s)", strings.Join(mc.Disabled, ", "))
	}
	if len(unknown) > 0 {
		detail += fmt.Sprintf(" — WARN: config names unregistered middleware (silently skipped): %s", strings.Join(unknown, ", "))
		return Check{Label: "Pipeline", Status: "WARN", Detail: detail}
	}
	return Check{Label: "Pipeline", Status: "OK", Detail: detail}
}

// CheckOTel checks OpenTelemetry configuration.
func CheckOTel(cfg *config.Config, cfgErr error) Check {
	if cfgErr != nil {
		return Check{Label: "Observability", Status: "WARN", Detail: "config not loaded"}
	}
	if !cfg.Observability.Otel.Enabled {
		return Check{Label: "Observability", Status: "OK", Detail: "OTel disabled"}
	}
	endpoint := cfg.Observability.Otel.Endpoint
	if ep := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); ep != "" {
		endpoint = ep
	}
	if endpoint == "" {
		return Check{Label: "Observability", Status: "WARN", Detail: "OTel enabled but no endpoint set"}
	}
	parts := []string{fmt.Sprintf("endpoint → %s", endpoint)}
	if cfg.Observability.Otel.Verbose {
		parts = append(parts, "verbose")
	}
	if cfg.Observability.Otel.Traces {
		parts = append(parts, "traces: on")
	} else {
		parts = append(parts, "traces: off")
	}
	if cfg.Observability.Otel.Metrics {
		parts = append(parts, "metrics: on")
	}
	return Check{Label: "Observability", Status: "OK", Detail: strings.Join(parts, ", ")}
}

// CheckHomeWritable checks if the home directory is writable.
func CheckHomeWritable() Check {
	home := os.Getenv("YAAH_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return Check{Label: "Home directory", Status: "FAIL", Detail: err.Error()}
		}
		home = filepath.Join(userHome, ".yaah")
	}

	if err := os.MkdirAll(home, 0o755); err != nil {
		return Check{Label: "Home directory writable", Status: "FAIL", Detail: err.Error()}
	}

	testFile := filepath.Join(home, ".doctor-write-test")
	if err := os.WriteFile(testFile, []byte("ok"), 0o644); err != nil {
		return Check{Label: "Home directory writable", Status: "FAIL", Detail: err.Error()}
	}
	os.Remove(testFile)

	return Check{Label: "Home directory writable", Status: "OK", Detail: home}
}

// CheckPlatform checks the platform information.
func CheckPlatform() Check {
	return Check{
		Label:  "Platform",
		Status: "OK",
		Detail: fmt.Sprintf("%s/%s %s", runtime.GOOS, runtime.GOARCH, runtime.Version()),
	}
}

// CheckEditor checks the editor configuration.
func CheckEditor(cfg *config.Config) Check {
	editor := config.ResolveEditor(cfg)

	// Determine the source for the detail line
	source := "default (vi)"
	if cfg != nil && cfg.Editor != "" {
		source = "config.yaml"
	} else if env := os.Getenv("EDITOR"); env != "" {
		source = "$EDITOR"
	} else if env := os.Getenv("VISUAL"); env != "" {
		source = "$VISUAL"
	}

	if editor == "vi" && source == "default (vi)" {
		return Check{
			Label:  "Editor",
			Detail: "not configured — set 'editor' in config.yaml or $EDITOR/$VISUAL (falling back to vi)",
			Status: "WARN",
		}
	}
	return Check{Label: "Editor", Status: "OK", Detail: fmt.Sprintf("%s (via %s)", editor, source)}
}

// resolveProviderName extracts the provider name from the config.
// This is a helper function used by multiple check functions.
func resolveProviderName(cfg *config.Config) string {
	// 1. Explicit default.provider setting
	if cfg.Agent.Default.Provider != "" {
		if _, ok := cfg.Providers[cfg.Agent.Default.Provider]; ok {
			return cfg.Agent.Default.Provider
		}
	}
	// 2. Provider/model prefix in default.model
	if parts := strings.SplitN(cfg.Agent.Default.Model, "/", 2); len(parts) == 2 {
		if _, ok := cfg.Providers[parts[0]]; ok {
			return parts[0]
		}
	}
	// 3. First provider alphabetically
	names := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) > 0 {
		return names[0]
	}
	return "local"
}

// Color helper functions - these respect NO_COLOR environment variable

// CheckShepherd reports the shepherd-tracing health (adoption plan
// R2): the store must open and a recovery must rebuild — and the
// recovery counts are the check's substance: scopes, roots, re-adopted
// worktrees, and the warn-only buckets (orphans, missing, unadopted)
// that surface leftover or lost work. Tracing is integral, so the
// resolved store path is always reported.
func CheckShepherd(cfg *config.Config, cfgErr error) Check {
	const label = "Shepherd tracing"
	if cfgErr != nil || cfg == nil {
		return Check{Label: label, Status: "WARN", Detail: "config unavailable"}
	}
	tracePath := cfg.Agent.Default.TracePath()
	// Runtime creates the store's parent directory before opening the
	// store; the diagnostic must not fail a state the next session start
	// would auto-heal (R2 review).
	traceDir := filepath.Dir(tracePath)
	if err := os.MkdirAll(traceDir, 0o700); err != nil {
		return Check{Label: label, Status: "FAIL", Detail: "trace dir " + traceDir + ": " + err.Error()}
	}
	store, err := shepherdtrace.NewSQLiteTraceStore(tracePath)
	if err != nil {
		return Check{Label: label, Status: "FAIL", Detail: "trace store " + tracePath + ": " + err.Error()}
	}
	defer store.Close(context.Background())
	repoPath := cfg.Agent.Default.RetainedRepoPath
	if repoPath == "" {
		repoPath, _ = os.Getwd()
	}
	if repoPath == "" {
		// The same condition runtime downgrades to a warning — the
		// row says the recovery was skipped, not that tracing broke.
		return Check{Label: label, Status: "WARN", Detail: "no repository resolved — recovery skipped"}
	}
	// adoptRoots: the doctor has no session, so the local-mode rule
	// applies; the throwaway registry is discarded either way and the
	// root counts do not depend on it.
	_, report, err := recovery.Recover(context.Background(), store, repoPath, true, nil)
	if err != nil {
		return Check{Label: label, Status: "FAIL", Detail: "recovery: " + err.Error()}
	}
	detail := fmt.Sprintf("recovery rebuilt %d scope(s) (%d root(s)), %d worktree(s) re-adopted",
		report.Scopes, report.Roots, len(report.Adopted))
	status := "OK"
	var warns []string
	if n := len(report.Orphans); n > 0 {
		warns = append(warns, fmt.Sprintf("%d orphaned worktree(s): %s", n, joinPaths(report.Orphans[0].Path, n)))
	}
	if n := len(report.Missing); n > 0 {
		warns = append(warns, fmt.Sprintf("%d missing worktree(s): %s", n, joinPaths(report.Missing[0].Path, n)))
	}
	if n := len(report.Unadopted); n > 0 {
		warns = append(warns, fmt.Sprintf("%d unadopted worktree(s): %s", n, joinPaths(report.Unadopted[0].Path, n)))
	}
	if len(warns) > 0 {
		status = "WARN"
		detail = detail + " — " + strings.Join(warns, "; ")
	}
	return Check{Label: label, Status: status, Detail: detail}
}

// joinPaths names the first path of a warn bucket and, when there are
// more, says so — the doctor row stays one line.
func joinPaths(first string, n int) string {
	if n > 1 {
		return fmt.Sprintf("%s (+%d more)", first, n-1)
	}
	return first
}

// StatusLabel returns the status string with appropriate color coding.
func StatusLabel(status string) string {
	if !doctorUseColor {
		return status
	}
	switch status {
	case "OK":
		return "\x1b[32m" + status + "\x1b[0m"
	case "WARN":
		return "\x1b[33m" + status + "\x1b[0m"
	case "FAIL":
		return "\x1b[31m" + status + "\x1b[0m"
	}
	return status
}

// GreenText returns the string in green color.
func GreenText(s string) string {
	if !doctorUseColor {
		return s
	}
	return "\x1b[32m" + s + "\x1b[0m"
}

// YellowText returns the string in yellow color.
func YellowText(s string) string {
	if !doctorUseColor {
		return s
	}
	return "\x1b[33m" + s + "\x1b[0m"
}

// DimText returns the string in dimmed color.
func DimText(s string) string {
	if !doctorUseColor {
		return s
	}
	return "\x1b[2m" + s + "\x1b[0m"
}
