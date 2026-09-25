package cli

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/emancu/pgdoctor/check"
)

func loadConfig(path string, checks []check.Package) (check.Config, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("reading config: %w", err)
	}

	var raw map[string]yaml.Node
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("parsing config %s: %w", path, err)
	}

	known := map[string]struct{}{}
	for _, pkg := range checks {
		known[pkg.Metadata().CheckID] = struct{}{}
	}

	cfg := check.Config{}
	var skipped []string
	for checkID, node := range raw {
		if _, ok := known[checkID]; !ok {
			skipped = append(skipped, fmt.Sprintf("config: skipping unknown check %q", checkID))
			continue
		}
		var settings map[string]yaml.Node
		if node.Kind != yaml.MappingNode || node.Decode(&settings) != nil {
			skipped = append(skipped, fmt.Sprintf("config: skipping %s: not a mapping", checkID))
			continue
		}
		cfg[checkID] = map[string]string{}
		for key, node := range settings {
			if node.Kind != yaml.ScalarNode {
				skipped = append(skipped, fmt.Sprintf("config: skipping %s.%s: not a scalar value", checkID, key))
				continue
			}
			cfg[checkID][key] = node.Value
		}
	}
	return cfg, skipped, nil
}
