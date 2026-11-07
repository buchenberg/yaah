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
	// MaxIterations is the raw agents.default.max_iterations value;
	// 0 = key absent.
	MaxIterations int
	// MaxTurns is the raw agents.default.max_turns value; 0 = key absent.
	MaxTurns int
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
				MaxIterations int `yaml:"max_iterations"`
				MaxTurns      int `yaml:"max_turns"`
			} `yaml:"default"`
		} `yaml:"agents"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return ExplicitDefaultBudget{}
	}
	return ExplicitDefaultBudget{
		MaxIterations: probe.Agents.Default.MaxIterations,
		MaxTurns:      probe.Agents.Default.MaxTurns,
	}
}
