package cli

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/emancu/pgdoctor/check"
)

var linePrefix = regexp.MustCompile(`^line \d+: `)

func loadConfig(path string, checks []check.Package) (check.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var raw map[string]yaml.Node
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}

	known := map[string]check.Package{}
	for _, pkg := range checks {
		known[pkg.Metadata().CheckID] = pkg
	}

	cfg := check.Config{}
	var problems []string
	for checkID, node := range raw {
		pkg, ok := known[checkID]
		if !ok {
			problems = append(problems, fmt.Sprintf("unknown check %q", checkID))
			continue
		}
		if pkg.DecodeConfig == nil {
			if node.Kind != yaml.MappingNode || len(node.Content) > 0 {
				problems = append(problems, fmt.Sprintf("%s: the check accepts no settings", checkID))
			}
			continue
		}
		// Decode rejects an anchor that contains itself and excessive aliasing.
		// resolveAliases does not stop on the first and expands the second in full.
		if err := node.Decode(new(any)); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", checkID, err))
			continue
		}
		resolveAliases(&node)
		settings, err := yaml.Marshal(&node)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", checkID, err))
			continue
		}
		value, err := pkg.DecodeConfig(settings)
		var typeErr *yaml.TypeError
		if errors.As(err, &typeErr) {
			for _, msg := range typeErr.Errors {
				// Line numbers count from the re-encoded section, not from the file.
				problems = append(problems, fmt.Sprintf("%s: %s", checkID, linePrefix.ReplaceAllString(msg, "")))
			}
			continue
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", checkID, err))
			continue
		}
		cfg[checkID] = value
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("invalid config %s:\n  %s", path, strings.Join(problems, "\n  "))
	}
	return cfg, nil
}

func resolveAliases(n *yaml.Node) {
	if n.Kind == yaml.AliasNode {
		*n = *n.Alias
	}
	for _, child := range n.Content {
		resolveAliases(child)
	}
}
