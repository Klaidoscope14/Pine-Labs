// Package scenario loads the hand-written scenarios used by stub agents and tests.
package scenario

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Scenario is one evals/scenarios/<name>/scenario.yaml.
type Scenario struct {
	Name            string   `yaml:"name"`
	Description     string   `yaml:"description"`
	Tenant          string   `yaml:"tenant"`
	Attempts        []string `yaml:"attempts"`
	DeveloperResult string   `yaml:"developer_result"`
	Approve         bool     `yaml:"approve"`
	Expect          struct {
		Status       string `yaml:"status"`
		Attempts     int    `yaml:"attempts"`
		ResolvedRule string `yaml:"resolved_rule"`
	} `yaml:"expect"`

	Dir string `yaml:"-"`
}

var safeName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Load reads one scenario by name.
func Load(root, name string) (Scenario, error) {
	if !safeName.MatchString(name) {
		return Scenario{}, fmt.Errorf("invalid scenario name %q", name)
	}
	dir := filepath.Join(root, name)
	raw, err := os.ReadFile(filepath.Join(dir, "scenario.yaml"))
	if err != nil {
		return Scenario{}, err
	}
	var s Scenario
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return Scenario{}, fmt.Errorf("scenario %s: %w", name, err)
	}
	if s.Name != name {
		return Scenario{}, fmt.Errorf("scenario %s: name field is %q", name, s.Name)
	}
	if len(s.Attempts) == 0 && s.DeveloperResult == "" {
		return Scenario{}, fmt.Errorf("scenario %s: needs attempts or developer_result", name)
	}
	s.Dir = dir
	return s, nil
}

// All lists every scenario directory (skipping _common).
func All(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && safeName.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// Common reads a shared file from <root>/_common, overridden by the scenario's own copy.
func (s Scenario) Common(root, file string) ([]byte, error) {
	if data, err := os.ReadFile(filepath.Join(s.Dir, file)); err == nil {
		return data, nil
	}
	return os.ReadFile(filepath.Join(root, "_common", file))
}

// Patch returns the patch for a 1-based attempt; later attempts reuse the last patch.
func (s Scenario) Patch(attempt int) ([]byte, error) {
	if len(s.Attempts) == 0 {
		return nil, fmt.Errorf("scenario %s has no patches", s.Name)
	}
	i := attempt - 1
	if i >= len(s.Attempts) {
		i = len(s.Attempts) - 1
	}
	return os.ReadFile(filepath.Join(s.Dir, s.Attempts[i]))
}
