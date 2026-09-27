package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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

	dec := yaml.NewDecoder(bytes.NewReader(data))
	var raw map[string]yaml.Node
	if err := dec.Decode(&raw); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}
	if err := dec.Decode(&yaml.Node{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parsing config %s: expected one YAML document", path)
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
		// Decode rejects an anchor that contains itself and excessive aliasing, but it
		// skips a merged key that an explicit key overrides, so resolveAliases keeps
		// its own limit.
		if err := node.Decode(new(any)); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", checkID, err))
			continue
		}
		expanded := 0
		resolved, err := resolveAliases(&node, false, &expanded)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", checkID, err))
			continue
		}
		settings, err := yaml.Marshal(resolved)
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

// maxAliasNodes bounds the nodes that alias expansion reaches in one check
// section. A cycle or an alias bomb never finishes without it.
const maxAliasNodes = 10000

// resolveAliases returns a copy of n in which each alias is replaced by its
// anchor. It never changes n, because check sections share the nodes of an anchor.
func resolveAliases(n *yaml.Node, inAlias bool, expanded *int) (*yaml.Node, error) {
	if n.Kind == yaml.AliasNode {
		n = n.Alias
		inAlias = true
	}
	if inAlias {
		*expanded++
		if *expanded > maxAliasNodes {
			return nil, errors.New("YAML aliases expand too far (an alias cycle or too many aliases)")
		}
	}
	resolved := *n
	resolved.Content = make([]*yaml.Node, len(n.Content))
	for i, child := range n.Content {
		var err error
		if resolved.Content[i], err = resolveAliases(child, inAlias, expanded); err != nil {
			return nil, err
		}
	}
	return &resolved, nil
}
