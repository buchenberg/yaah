package acp

import (
	"strings"

	"github.com/buchenberg/yaah/internal/agent/subagent"
)

// ModelOption is one selectable model, as the injected model catalog
// (the ModelCatalog passed to NewServer) reports it. The wire value is
// "<provider>/<model>": provider names never contain a slash, while
// model ids may ("openrouter/anthropic/claude-3.5-sonnet"), so a value
// is split on its FIRST slash only.
//
// Name and Description are optional display text; an empty Name falls
// back to the model id.
type ModelOption struct {
	Provider    string
	Model       string
	Name        string
	Description string
}

// modelValue renders one model as its select value. A missing provider
// or model name yields "" rather than an ambiguous "provider/" or
// "/model" value that could never be split back.
func modelValue(provider, model string) string {
	if provider == "" || model == "" {
		return ""
	}
	return provider + "/" + model
}

// splitModelValue splits an advertised model value into its provider
// and model. Only the FIRST slash separates them: model ids carry
// slashes of their own, so splitting on the last one would report
// "openrouter/anthropic" as the provider.
func splitModelValue(value string) (provider, model string, ok bool) {
	i := strings.IndexByte(value, '/')
	if i <= 0 || i == len(value)-1 {
		return "", "", false
	}
	return value[:i], value[i+1:], true
}

// buildModelConfigOptions renders the model picker from a model catalog
// and the session's current selection. It is the single place that
// option is built, so the initial state (session/new, session/load) and
// the recomputed state session/set_config_option echoes can never
// disagree.
//
// A nil or empty catalog yields nil, which omits configOptions from the
// result entirely: a picker with nothing to pick would make the client
// hang on a menu that cannot change anything.
//
// The session's current model is always one of the returned options.
// When the catalog does not list it — a model dropped from the config,
// or a session restored with a model the provider no longer advertises —
// it is prepended with the requested provider and model names, because
// the spec requires currentValue to name a value from its own options.
func buildModelConfigOptions(catalog []ModelOption, provider, model string) []SessionConfigOption {
	if len(catalog) == 0 {
		return nil
	}
	current := modelValue(provider, model)
	options := make([]SessionConfigSelectOption, 0, len(catalog)+1)
	listed := make(map[string]bool, len(catalog)+1)
	for _, m := range catalog {
		value := modelValue(m.Provider, m.Model)
		if value == "" || listed[value] {
			continue
		}
		listed[value] = true
		label := m.Name
		if label == "" {
			label = m.Model
		}
		options = append(options, SessionConfigSelectOption{
			Value:       value,
			Name:        label,
			Description: m.Description,
		})
	}
	if len(options) == 0 {
		return nil
	}
	switch {
	case current == "":
		// No resolved model on the session yet: fall back to the first
		// advertised option so currentValue still names its own value.
		current = options[0].Value
	case !listed[current]:
		options = append([]SessionConfigSelectOption{{
			Value:       current,
			Name:        model,
			Description: "current model, not in the discovery catalog",
		}}, options...)
	}
	return []SessionConfigOption{{
		ID:           ConfigOptionModelID,
		Name:         "Model",
		Description:  "Model used for subsequent prompts",
		Category:     ConfigOptionCategoryModel,
		Type:         ConfigOptionTypeSelect,
		CurrentValue: current,
		Options:      options,
	}}
}

// buildModeConfigOptions renders the mode-axis selector (read-only vs
// full; wire ids build/plan). This is the only mode advertisement: yaah
// does not emit the legacy session-modes field.
func buildModeConfigOptions(mode string) SessionConfigOption {
	if !validSessionMode(mode) {
		mode = SessionModeBuild
	}
	options := make([]SessionConfigSelectOption, 0, len(availableSessionModes()))
	for _, m := range availableSessionModes() {
		options = append(options, SessionConfigSelectOption{
			Value:       m.ID,
			Name:        m.Name,
			Description: m.Description,
		})
	}
	return SessionConfigOption{
		ID:           ConfigOptionModeID,
		Name:         "Session Mode",
		Description:  "Read-only with the inspection set, or full access",
		Category:     ConfigOptionCategoryMode,
		Type:         ConfigOptionTypeSelect,
		CurrentValue: mode,
		Options:      options,
	}
}

// buildAgentConfigOptions renders the agent-axis selector: every
// primary-capable persona the registry offers, with the session's
// current persona as the current value. The current persona is always
// one of the options: a session restored with a persona that has since
// left the registry (file deleted, mode changed) is prepended, because
// currentValue must name a value from the option's own list.
func buildAgentConfigOptions(personas []subagent.Persona, current string) SessionConfigOption {
	options := make([]SessionConfigSelectOption, 0, len(personas)+1)
	listed := make(map[string]bool, len(personas)+1)
	for _, p := range personas {
		if p.Name == "" || listed[p.Name] {
			continue
		}
		listed[p.Name] = true
		options = append(options, SessionConfigSelectOption{
			Value:       p.Name,
			Name:        p.Name,
			Description: p.Description,
		})
	}
	if current != "" && !listed[current] {
		options = append([]SessionConfigSelectOption{{
			Value:       current,
			Name:        current,
			Description: "current agent, no longer in the registry",
		}}, options...)
	}
	if len(options) == 0 {
		// No personas at all (no registry, hand-built session): a select
		// with no options cannot be rendered, so advertise the current
		// name alone — or "build" when there is not even that.
		if current == "" {
			current = subagent.BuildPersonaName
		}
		options = []SessionConfigSelectOption{{Value: current, Name: current}}
		listed[current] = true
	}
	if current == "" {
		current = subagent.BuildPersonaName
	}
	return SessionConfigOption{
		ID:           ConfigOptionAgentID,
		Name:         "Agent",
		Description:  "Persona the session runs as",
		Category:     ConfigOptionCategoryAgent,
		Type:         ConfigOptionTypeSelect,
		CurrentValue: current,
		Options:      options,
	}
}

// buildApprovalConfigOptions renders the approval-axis selector: the
// posture dangerous tool calls meet. The value is one of the loop's
// approval modes; an unknown current value falls back to "ask" so the
// advertised set never names a value it does not offer.
func buildApprovalConfigOptions(current string) SessionConfigOption {
	switch current {
	case "allow", "ask", "deny":
	default:
		current = "ask"
	}
	return SessionConfigOption{
		ID:           ConfigOptionApprovalID,
		Name:         "Approval",
		Description:  "How dangerous tool calls are approved",
		Category:     ConfigOptionCategoryApproval,
		Type:         ConfigOptionTypeSelect,
		CurrentValue: current,
		Options: []SessionConfigSelectOption{
			{Value: "allow", Name: "Allow", Description: "Run dangerous calls without asking."},
			{Value: "ask", Name: "Ask", Description: "Ask before every dangerous call."},
			{Value: "deny", Name: "Deny", Description: "Refuse dangerous calls."},
		},
	}
}

// buildSessionConfigOptions returns the full selector set in priority
// order: the agent, mode and approval axes (the three session axes of
// decouple-persona-mode-approval), then the model picker when a catalog
// is wired. The axis values come from the session's current state, so
// every advertisement — session/new, session/load, and the echoes a
// set_config_option reply recomputes — agrees by construction.
func buildSessionConfigOptions(catalog []ModelOption, provider, model, personaName string, readOnly bool, approval string) []SessionConfigOption {
	mode := SessionModeBuild
	if readOnly {
		mode = SessionModePlan
	}
	out := []SessionConfigOption{
		buildAgentConfigOptions(subagent.Personas(), personaName),
		buildModeConfigOptions(mode),
		buildApprovalConfigOptions(approval),
	}
	out = append(out, buildModelConfigOptions(catalog, provider, model)...)
	return out
}

// configOptionHasValue reports whether the config option named id
// offers value in one of its options. An absent option, or one with no
// options at all, has no values.
func configOptionHasValue(options []SessionConfigOption, id, value string) bool {
	for _, opt := range options {
		if opt.ID != id {
			continue
		}
		for _, v := range opt.Options {
			if v.Value == value {
				return true
			}
		}
	}
	return false
}
