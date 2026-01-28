package claude

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config holds the Claude AI integration configuration.
type Config struct {
	// MaxLines is the maximum number of diff lines to send to Claude.
	MaxLines int `yaml:"maxLines"`

	// IgnorePatterns is a list of glob patterns for files to exclude.
	IgnorePatterns []string `yaml:"ignorePatterns"`

	// Prompts contains the prompt templates for different operations.
	Prompts Prompts `yaml:"prompts"`

	// RefineOptions is a list of quick refinement options.
	RefineOptions []RefineOption `yaml:"refineOptions"`
}

// Prompts contains prompt templates for Claude operations.
type Prompts struct {
	// Review is the prompt template for code review.
	Review string `yaml:"review"`

	// Summary is the prompt template for PR summary generation.
	Summary string `yaml:"summary"`

	// Commit is the prompt template for commit message generation.
	Commit string `yaml:"commit"`

	// StackReview is the prompt template for stack review.
	StackReview string `yaml:"stackReview"`
}

// RefineOption is a quick refinement option for user selection.
type RefineOption struct {
	// Label is the display label for this option.
	Label string `yaml:"label"`

	// Prompt is the instruction to append for refinement.
	Prompt string `yaml:"prompt"`
}

// DefaultConfig returns the default configuration.
func DefaultConfig() *Config {
	return &Config{
		MaxLines: 4000,
		IgnorePatterns: []string{
			"*.lock",
			"*.sum",
			"*.min.js",
			"*.min.css",
			"*.svg",
			"*.pb.go",
			"*_generated.go",
			"vendor/*",
			"node_modules/*",
		},
		Prompts: Prompts{
			Review:      defaultReviewPrompt,
			Summary:     defaultSummaryPrompt,
			Commit:      defaultCommitPrompt,
			StackReview: defaultStackReviewPrompt,
		},
		RefineOptions: []RefineOption{
			{
				Label:  "Make it shorter",
				Prompt: "Under 100 words.",
			},
			{
				Label:  "Conventional commits",
				Prompt: "Use feat:/fix:/docs:/chore: format.",
			},
			{
				Label:  "More detail",
				Prompt: "Add more technical detail about the implementation.",
			},
			{
				Label:  "Focus on why",
				Prompt: "Focus more on why these changes are needed.",
			},
		},
	}
}

// DefaultConfigPath returns the default configuration file path.
func DefaultConfigPath() string {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "git-spice", "claude.yaml")
}

// LoadConfig loads configuration from the given path.
// If the file does not exist, returns the default configuration.
func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config file: %w", err)
	}

	// Parse into a separate struct to merge with defaults.
	var fileCfg Config
	if err := yaml.Unmarshal(data, &fileCfg); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	// Merge file config with defaults.
	if fileCfg.MaxLines != 0 {
		cfg.MaxLines = fileCfg.MaxLines
	}
	if len(fileCfg.IgnorePatterns) > 0 {
		cfg.IgnorePatterns = fileCfg.IgnorePatterns
	}
	if fileCfg.Prompts.Review != "" {
		cfg.Prompts.Review = fileCfg.Prompts.Review
	}
	if fileCfg.Prompts.Summary != "" {
		cfg.Prompts.Summary = fileCfg.Prompts.Summary
	}
	if fileCfg.Prompts.Commit != "" {
		cfg.Prompts.Commit = fileCfg.Prompts.Commit
	}
	if fileCfg.Prompts.StackReview != "" {
		cfg.Prompts.StackReview = fileCfg.Prompts.StackReview
	}
	if len(fileCfg.RefineOptions) > 0 {
		cfg.RefineOptions = fileCfg.RefineOptions
	}

	return cfg, nil
}

// Validate checks if the configuration is valid.
func (c *Config) Validate() error {
	if c.MaxLines <= 0 {
		return errors.New("maxLines must be positive")
	}
	return nil
}

const defaultReviewPrompt = `Review PR: "{title}"

## Guidelines
1. Code Quality - readability, naming, structure
2. Functionality - correctness, edge cases, bugs
3. Performance - efficiency, memory

## Output
Use suggestion blocks. Be succinct, direct, actionable.

### Changes Requested
- [ ] ...

## Diff:
{diff}`

const defaultSummaryPrompt = `Generate PR title and description for the following changes.
Branch: {branch}, Base: {base}
Commits: {commits}
Diff: {diff}

Use this format:
TITLE: <max 72 chars, imperative mood>
BODY:
# Summary

## Why
Describe *why* this PR is needed.

## What
Describe *what* this PR does.

## Test Plan
- [ ] Build passes with this PR
- [ ] Unit tests pass
- [ ] Ran offline tests (describe commands)
- [ ] Ran online tests (if applicable)`

const defaultCommitPrompt = `Generate commit message for staged changes.
Diff: {diff}
Format: SUBJECT: <72 chars>, BODY: <details>`

const defaultStackReviewPrompt = `Review this stack. Per-branch summary, then full stack summary.
{branches}`
