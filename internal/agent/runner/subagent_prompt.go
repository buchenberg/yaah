package runner

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/buchenberg/yaah/internal/agent/subagent"
	"github.com/buchenberg/yaah/internal/prompts"
	"github.com/buchenberg/yaah/internal/tools"
)

// buildSubAgentSysPrompt assembles the full system prompt for a sub-agent
// loop. Section order: environment header, identity-derived base prompt,
// per-role directives (from agent.subagent.roles.<role>.directives), the
// isolated-workspace delta (isolated sessions only, decision D2), role
// guidance, response contract, escalation format, and shell anchoring.
// Directives sit immediately after the base prompt so they stay prominent
// instead of being buried behind role guidance and format blocks.
//
// Shell guidance is gated on the role's tool set (#329): a role without a
// shell tool gets an environment header with no shell sentence, and a
// zero-tool role's base prompt swaps the tool-oriented sub-agent identity
// for the no-tools variant whose directive leads the identity block.
func buildSubAgentSysPrompt(opts taskRunnerOpts, role subagent.SubAgentRole, profile subagent.RoleProfile, jsonMode bool, workdir string, conf *subAgentConfinement) string {
	cwd, _ := os.Getwd()

	// The tools this environment cannot offer (yaah-9n0): the shell
	// header and the available-shell block must describe what the
	// registry actually holds, not what the role asked for.
	unavailable := subAgentUnavailableTools(opts, profile, workdir)
	effectiveTools := profile.Tools
	if len(unavailable) > 0 {
		dropped := make(map[string]bool, len(unavailable))
		for _, name := range unavailable {
			dropped[name] = true
		}
		effectiveTools = make([]string, 0, len(profile.Tools))
		for _, name := range profile.Tools {
			if !dropped[name] {
				effectiveTools = append(effectiveTools, name)
			}
		}
	}

	// The environment header must advertise the directory the sub-agent's
	// tools actually resolve against. In an isolated session that is the
	// sandbox workdir, not the host cwd — the header's working-directory
	// line is what shell-cd'ing and relative paths key off, and a sandboxed
	// tool rejects the advertised host path as an escape. The shell line
	// comes from the workspace too: the container's shell is sh, not the
	// host's bash/powershell. A local-mode workdir dispatch (a supervised
	// session's fork variant) gets the same treatment: its file tools and
	// shell are rooted at the assigned worktree, so advertising the
	// orchestrator's cwd would point the model at the wrong tree (#312).
	envOS, envArch, envShell, envCwd := runtime.GOOS, runtime.GOARCH, "", cwd
	if opts.workspace != nil {
		envShell, _ = opts.workspace.Shell()
		envCwd = opts.workspace.WorkDir()
	} else {
		if workdir != "" {
			envCwd = workdir
		}
		if runtime.GOOS == "windows" {
			envShell = "powershell (pwsh 7+ or Windows PowerShell)"
		} else {
			envShell = "bash"
		}
	}
	// A role without a shell tool must not receive the header's "use it
	// for all shell commands" imperative (#329): the registry gives it no
	// shell, and tool-less roles resolve the contradiction by fabricating
	// fenced command blocks with invented output. An empty shell drops the
	// sentence; the OS and working-directory lines stay.
	if !roleHasShell(effectiveTools) {
		envShell = ""
	}

	// A zero-tool role gets the no-tools identity variant: the directive
	// sits before the identity text so it lands early in the prompt, and
	// the tool-batching cardinal rule and named-tool guidance disappear.
	base := opts.systemPrompt
	if len(effectiveTools) == 0 {
		base = prompts.InjectNoToolsIdentity(base)
	}
	// Section order is a caching contract (yaah-o02): the prompt opens
	// with the byte-stable block — identity, directives, guidance,
	// contract, escalation, shell rules — so sibling dispatches and later
	// turns of one dispatch share the longest possible provider
	// prompt-cache prefix. The volatile parts close it: confinement
	// guidance, then the environment header with its per-dispatch
	// working directory.
	sysPrompt := base

	if ds := opts.subCfg.Roles[string(role)].Directives; len(ds) > 0 {
		sysPrompt += "\n\n" + prompts.FormatDirectives(ds)
	}

	// Isolated session (decision D2): the sub-agent shares the session's
	// container, so its guidance must say so — without it a sub-agent
	// claims its changes are saved, or retries a host-only tool its
	// profile names that isolated mode omitted. Placed after the
	// directives block, which the section order below pins immediately
	// after the base prompt.
	if opts.workspace != nil {
		var dropped []string
		for _, name := range profile.Tools {
			if tools.LeafToolIsHostOnly(name) {
				dropped = append(dropped, name)
			}
		}
		sysPrompt += "\n\n" + prompts.IsolatedSubAgentGuidance(dropped)
	}

	if g := subagent.RoleGuidance(role); g != "" {
		if sysPrompt != "" {
			sysPrompt += "\n\n"
		}
		sysPrompt += g
	}
	if g := prompts.SubAgentUnavailableTools(unavailable); g != "" {
		sysPrompt += "\n\n" + g
	}

	if profile.Contract.Heading != "" && len(profile.Contract.Fields) > 0 {
		var b strings.Builder
		if jsonMode {
			b.WriteString("\n\nRespond with a JSON object matching the contract below.\n\n")
		}
		b.WriteString(prompts.ContractRules())
		b.WriteString("\n\n## Response contract\n\n")
		b.WriteString("Always end your response with a structured block:\n\n```\n")
		b.WriteString(profile.Contract.Heading + "\n")
		for _, f := range profile.Contract.Fields {
			if f.Kind != "" {
				b.WriteString("- **" + f.Name + "** (" + f.Kind + "): <value>\n")
			} else {
				b.WriteString("- **" + f.Name + "**: <value>\n")
			}
		}
		b.WriteString("```")
		sysPrompt += b.String()
	}

	// Escalation contract: all sub-agents must know how to raise
	// structured escalations when they hit a blocker.
	sysPrompt += prompts.Escalation()

	if roleHasShell(effectiveTools) {
		shell := "bash"
		if runtime.GOOS == "windows" {
			shell = "powershell"
		}
		otherShell := "powershell"
		if runtime.GOOS == "windows" {
			otherShell = "bash, sh, or cmd"
		}
		if opts.workspace != nil {
			// The isolated workspace's contract (D5) guarantees sh
			// — bash may exist in the image, but only sh is promised,
			// and the shell the workspace actually launches is what
			// the header below already advertises. The two statements
			// must agree, or the model flips between them.
			shell, _ = opts.workspace.Shell()
			otherShell = "bash or powershell — only " + shell + " is guaranteed in the container"
		}
		sysPrompt += "\n\n## Available Shell\n"
		sysPrompt += fmt.Sprintf("Your environment only has %s available. ", shell)
		sysPrompt += fmt.Sprintf("Use %s for ALL command execution. ", shell)
		sysPrompt += fmt.Sprintf("There is no %s.", otherShell)
	}

	// A confined dispatch gets told the boundary before it meets it (Plan 7
	// Phase 3): the one writable root, the redirect, and the git-write rule that
	// the default flip makes reachable. Enforced, not merely requested — a
	// paddock placement with no usable backend refuses at Exec, so there is no
	// run to advise. This and the environment header below are the prompt's
	// only per-dispatch sections; both name the assigned worktree.
	// The egress half follows the POSTURE (a dispatch refused a jail still must not
	// reach the network from outside one); the jail half follows enforcement. The
	// function returns "" when it has nothing to say, so the block is only added
	// when it does.
	if opts.workspace == nil {
		// The posture comes from the POLICY: the confinement record carries it only
		// when the jail was established, so reading it from conf would suppress the
		// egress half exactly when the jail was refused — while the tools are still
		// omitted.
		network, broker := networkMode(opts.subNetwork), opts.subNetwork.BrokerPort
		if g := prompts.ConfinementSubAgentGuidance(workdir, network, broker,
			hostNetworkToolsIn(profile), conf != nil && conf.Enforced); g != "" {
			if sysPrompt != "" {
				sysPrompt += "\n\n"
			}
			sysPrompt += g
		}
	} else if restrictivePosture(opts.subNetwork) {
		// The container's netns covers its shell children, not the HTTP clients
		// running in yaah's process. Say why the tools are absent without
		// claiming the container itself enforces a posture it may not.
		if omitted := hostNetworkToolsIn(profile); len(omitted) > 0 {
			sysPrompt += "\n\nThe tools `" + strings.Join(omitted, "`, `") +
				"` are not available under a restrictive network posture: they make their requests in the yaah host process, outside the container network namespace."
		}
	}

	sysPrompt += "\n\n" + prompts.EnvironmentHeader(envOS, envArch, envShell, envCwd)
	return sysPrompt
}
