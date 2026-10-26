package acp

import "strings"

// ModelOption is one selectable model, as the injected model catalog
// (Server.ModelCatalog) reports it. The wire value is
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
