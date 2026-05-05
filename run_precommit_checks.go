package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"go.abhg.dev/gs/internal/claude"
	"go.abhg.dev/gs/internal/git"
	"go.abhg.dev/gs/internal/runlocal"
	"go.abhg.dev/gs/internal/silog"
)

// runPrecommitChecksCmd runs configured local checks before push.
type runPrecommitChecksCmd struct {
	Only string `help:"Comma-separated list of check names to run (default: all)"`
	Fix  bool   `help:"On failure, hand captured output to Claude for diagnosis"`
}

func (cmd *runPrecommitChecksCmd) Run(
	ctx context.Context,
	log *silog.Logger,
	wt *git.Worktree,
) error {
	// Load configured checks from the repo root.
	checks, err := runlocal.Load(wt.RootDir())
	if err != nil {
		return fmt.Errorf("load checks: %w", err)
	}

	// Filter by name if --only is specified.
	if cmd.Only != "" {
		allowed := make(map[string]struct{})
		for name := range strings.SplitSeq(cmd.Only, ",") {
			allowed[strings.TrimSpace(name)] = struct{}{}
		}
		var filtered []runlocal.Check
		for _, c := range checks {
			if _, ok := allowed[c.Name]; ok {
				filtered = append(filtered, c)
			}
		}
		checks = filtered
	}

	// Run all checks, streaming output to stderr.
	results, err := runlocal.DefaultRunner{}.Run(ctx, checks, os.Stderr)
	if err != nil {
		return fmt.Errorf("run checks: %w", err)
	}

	// Collect failed results.
	var failed []runlocal.Result
	for _, r := range results {
		if r.ExitCode != 0 {
			failed = append(failed, r)
		}
	}
	if len(failed) == 0 {
		return nil
	}

	// Optionally diagnose failures with Claude.
	if cmd.Fix {
		if err := cmd.diagnoseWithClaude(ctx, log, failed); err != nil {
			log.Warn("Claude diagnosis unavailable", "error", err)
		}
	}

	return fmt.Errorf("%d check(s) failed", len(failed))
}

// diagnoseWithClaude sends failed check output to Claude for diagnosis.
func (cmd *runPrecommitChecksCmd) diagnoseWithClaude(
	ctx context.Context,
	log *silog.Logger,
	failed []runlocal.Result,
) error {
	// Initialize Claude client.
	client := claude.NewClient(nil)
	if !client.IsAvailable() {
		return errors.New(
			"claude CLI not found; " +
				"please install it from https://claude.ai/download",
		)
	}

	// Load configuration.
	cfg, err := claude.LoadConfig(claude.DefaultConfigPath())
	if err != nil {
		log.Warn("Could not load claude config, using defaults", "error", err)
		cfg = claude.DefaultConfig()
	}

	// Build a prompt summarizing all failed checks.
	var sb strings.Builder
	sb.WriteString("The following pre-commit checks failed. " +
		"Please diagnose the failures and suggest fixes.\n\n")
	for _, r := range failed {
		fmt.Fprintf(&sb, "## Check: %s (exit code %d)\n\n", r.Name, r.ExitCode)
		if r.Output != "" {
			sb.WriteString("```\n")
			sb.WriteString(r.Output)
			sb.WriteString("```\n\n")
		}
		if r.Err != nil {
			fmt.Fprintf(&sb, "Error: %v\n\n", r.Err)
		}
	}

	// Send to Claude and print the response.
	fmt.Fprint(os.Stdout, "Sending to Claude for diagnosis... ")
	response, err := client.SendPromptWithModel(
		ctx, sb.String(), cfg.Models.Review,
	)
	fmt.Fprintln(os.Stdout, "done")
	if err != nil {
		return fmt.Errorf("claude: %w", err)
	}

	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintln(os.Stdout, "=== Claude Diagnosis ===")
	fmt.Fprintln(os.Stdout, "")
	fmt.Fprintln(os.Stdout, response)

	return nil
}
