package cli

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/emancu/pgdoctor/check"
)

func loadConfig(path string, checks []check.Package) (check.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var raw map[string]map[string]yaml.Node
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}

	known := map[string]struct{}{}
	for _, pkg := range checks {
		known[pkg.Metadata().CheckID] = struct{}{}
	}

	cfg := check.Config{}
	for checkID, settings := range raw {
		if _, ok := known[checkID]; !ok {
			return nil, fmt.Errorf("config %s: unknown check %q", path, checkID)
		}
		cfg[checkID] = map[string]string{}
		for key, node := range settings {
			if node.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("config %s: %s.%s must be a scalar value", path, checkID, key)
			}
			cfg[checkID][key] = node.Value
		}
	}
	return cfg, nil
}
