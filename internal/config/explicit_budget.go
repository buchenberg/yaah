package config

import "gopkg.in/yaml.v3"

// ExplicitDefaultBudget records the agents.default budget keys the
// config file set explicitly (yaah-21uk.8). Load decodes the file into
// the shipped defaults, which makes an explicit max_iterations: 50
// indistinguishable from the built-in default once merged — persona
// budget resolution (cmd/yaah resolveOrchestratorBudget) needs the
// distinction: an explicit config value always wins over a persona
// file's, while an unset key lets the persona file fill the gap.
// Zero means the key was absent (or the Config was built by hand
// rather than loaded), which resolves today's limits unchanged.
type ExplicitDefaultBudget struct {
	// MaxIterations is the raw agents.default.max_iterations value.
	MaxIterations int
	// MaxIterationsSet reports the key was present in the file: an
	// explicit 0 (cycles off) must win over a persona file's budget, so
	// presence and value are tracked separately.
	MaxIterationsSet bool
	// MaxTurns is the raw agents.default.max_turns value.
	MaxTurns    int
	MaxTurnsSet bool
}

// captureExplicitDefaultBudget re-decodes the raw config bytes into a
// minimal probe so the explicit values survive the defaults merge in
// Load. The full parse has already validated the file, so a decode
// failure here is unreachable in practice; the zero value (nothing
// explicit) is the safe degradation either way.
func captureExplicitDefaultBudget(data []byte) ExplicitDefaultBudget {
	var probe struct {
		Agents struct {
			Default struct {
				MaxIterations *int `yaml:"max_iterations"`
				MaxTurns      *int `yaml:"max_turns"`
			} `yaml:"default"`
		} `yaml:"agents"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return ExplicitDefaultBudget{}
	}
	out := ExplicitDefaultBudget{}
	if v := probe.Agents.Default.MaxIterations; v != nil {
		out.MaxIterations, out.MaxIterationsSet = *v, true
	}
	if v := probe.Agents.Default.MaxTurns; v != nil {
		out.MaxTurns, out.MaxTurnsSet = *v, true
	}
	return out
}
