package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/emancu/pgdoctor/check"
	"github.com/emancu/pgdoctor/checks/sessionsettings"
	"github.com/emancu/pgdoctor/checks/tablevacuumhealth"
)

// A check that reads settings must be listed here, or loadConfig rejects
// every key of that check as unknown.
var settingValidators = map[string]func(key, value string) error{
	sessionsettings.Metadata().CheckID:   sessionsettings.ValidateSetting,
	tablevacuumhealth.Metadata().CheckID: tablevacuumhealth.ValidateSetting,
}

func loadConfig(path string, checks []check.Package) (check.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var raw map[string]yaml.Node
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}

	known := map[string]struct{}{}
	for _, pkg := range checks {
		known[pkg.Metadata().CheckID] = struct{}{}
	}

	cfg := check.Config{}
	var problems []string
	for checkID, node := range raw {
		if _, ok := known[checkID]; !ok {
			problems = append(problems, fmt.Sprintf("unknown check %q", checkID))
			continue
		}
		var settings map[string]yaml.Node
		if node.Kind != yaml.MappingNode || node.Decode(&settings) != nil {
			problems = append(problems, fmt.Sprintf("%s: not a mapping", checkID))
			continue
		}
		cfg[checkID] = map[string]string{}
		for key, node := range settings {
			value, ok := settingValue(node)
			if !ok {
				problems = append(problems, fmt.Sprintf("%s.%s: not a scalar value", checkID, key))
				continue
			}
			validate, ok := settingValidators[checkID]
			if !ok {
				problems = append(problems, fmt.Sprintf("%s: unknown key %q", checkID, key))
				continue
			}
			if err := validate(key, value); err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", checkID, err))
				continue
			}
			cfg[checkID][key] = value
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("invalid config %s:\n  %s", path, strings.Join(problems, "\n  "))
	}
	return cfg, nil
}

func settingValue(node yaml.Node) (string, bool) {
	if node.Kind == yaml.ScalarNode {
		return node.Value, true
	}
	if node.Kind != yaml.SequenceNode {
		return "", false
	}
	items := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		if item.Kind != yaml.ScalarNode {
			return "", false
		}
		items = append(items, item.Value)
	}
	return strings.Join(items, ","), true
}
