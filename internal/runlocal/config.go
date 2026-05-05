package runlocal

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Load resolves the list of checks for the given repository root
// using the following precedence:
//
//  1. <repoRoot>/.gitspice/precommit.yaml — if present, parse and return.
//  2. <repoRoot>/.pre-commit-config.yaml — if present, return a single
//     check that delegates to "pre-commit run --all-files".
//  3. <repoRoot>/mise.toml — if it contains [tasks.lint], [tasks.test],
//     or [tasks.build] headers, build a Check per matching task
//     using "mise run <name>".
//  4. Fallback — hardcoded checks for lint, test, and build
//     via "mise run <name>".
func Load(repoRoot string) ([]Check, error) {
	// 1. Explicit gs config takes highest precedence.
	yamlPath := filepath.Join(repoRoot, ".gitspice", "precommit.yaml")
	switch _, err := os.Stat(yamlPath); {
	case err == nil:
		return loadFromYAML(yamlPath)
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("stat %s: %w", yamlPath, err)
	}

	// 2. Delegate to the pre-commit framework if its config is present.
	preCommitPath := filepath.Join(repoRoot, ".pre-commit-config.yaml")
	switch _, err := os.Stat(preCommitPath); {
	case err == nil:
		return preCommitChecks(), nil
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("stat %s: %w", preCommitPath, err)
	}

	// 3. Mise auto-detect via text scan.
	miseTomlPath := filepath.Join(repoRoot, "mise.toml")
	if data, err := os.ReadFile(miseTomlPath); err == nil {
		if checks := miseChecks(data); len(checks) > 0 {
			return checks, nil
		}
	}

	// 4. Hardcoded fallback.
	return fallbackChecks(), nil
}

// preCommitChecks returns a single check that delegates to the
// pre-commit framework. The framework handles per-hook orchestration,
// reporting, and exit-code aggregation; we just invoke it.
func preCommitChecks() []Check {
	return []Check{{
		Name: "pre-commit",
		Cmd:  "pre-commit run --all-files",
	}}
}

// yamlCheck is the on-disk representation of a check in precommit.yaml.
type yamlCheck struct {
	Name     string `yaml:"name"`
	Cmd      string `yaml:"cmd"`
	FailFast bool   `yaml:"fail_fast"`
	Timeout  string `yaml:"timeout"`
}

// yamlConfig is the top-level structure of precommit.yaml.
type yamlConfig struct {
	Checks []yamlCheck `yaml:"checks"`
}

// loadFromYAML reads and parses a precommit.yaml file.
func loadFromYAML(path string) ([]Check, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg yamlConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	checks := make([]Check, len(cfg.Checks))
	for i, yc := range cfg.Checks {
		c := Check{
			Name:     yc.Name,
			Cmd:      yc.Cmd,
			FailFast: yc.FailFast,
		}
		if yc.Timeout != "" {
			d, err := time.ParseDuration(yc.Timeout)
			if err != nil {
				return nil, fmt.Errorf(
					"check %q: parse timeout %q: %w",
					yc.Name, yc.Timeout, err,
				)
			}
			c.Timeout = d
		}
		checks[i] = c
	}
	return checks, nil
}

// _miseTaskOrder defines the fixed order in which mise tasks are considered.
var _miseTaskOrder = []string{"lint", "test", "build"}

// miseChecks scans mise.toml content for known task headers
// and returns a Check for each one found, in fixed order.
func miseChecks(data []byte) []Check {
	var checks []Check
	for _, name := range _miseTaskOrder {
		header := []byte("[tasks." + name + "]")
		if bytes.Contains(data, header) {
			checks = append(checks, Check{
				Name: name,
				Cmd:  "mise run " + name,
			})
		}
	}
	return checks
}

// fallbackChecks returns the hardcoded default checks.
func fallbackChecks() []Check {
	return []Check{
		{Name: "lint", Cmd: "mise run lint"},
		{Name: "test", Cmd: "mise run test"},
		{Name: "build", Cmd: "mise run build"},
	}
}
