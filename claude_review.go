package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.abhg.dev/gs/internal/claude"
	"go.abhg.dev/gs/internal/git"
	"go.abhg.dev/gs/internal/silog"
	"go.abhg.dev/gs/internal/spice"
	"go.abhg.dev/gs/internal/spice/state"
	"go.abhg.dev/gs/internal/text"
	"go.abhg.dev/gs/internal/ui"
)

type claudeReviewCmd struct {
	From      string `help:"Start of the range to review (defaults to trunk)"`
	To        string `help:"End of the range to review (defaults to current branch)"`
	PerBranch bool   `help:"Review each branch individually, then provide an overall summary"`
	Title     string `help:"Title for the review (defaults to branch name or range)"`
	Fix       bool   `help:"After review, prompt to apply suggested fixes"`
}

func (*claudeReviewCmd) Help() string {
	return text.Dedent(`
		Reviews code changes using Claude AI.

		By default, reviews all changes from the trunk to the current branch.
		Use --from and --to flags to specify a custom range.

		The --per-branch flag reviews each branch in the stack individually,
		then provides an overall summary of the entire stack.

		Example usage:
		  gs claude review                      # Review current branch against trunk
		  gs claude review --from main --to feature
		  gs claude review --per-branch         # Review each branch in stack
	`)
}

func (cmd *claudeReviewCmd) Run(
	ctx context.Context,
	log *silog.Logger,
	view ui.View,
	repo *git.Repository,
	wt *git.Worktree,
	store *state.Store,
	svc *spice.Service,
) error {
	// Initialize Claude client.
	client := claude.NewClient(nil)
	if !client.IsAvailable() {
		return errors.New("claude CLI not found; please install it from https://claude.ai/download")
	}

	// Load configuration.
	cfg, err := claude.LoadConfig(claude.DefaultConfigPath())
	if err != nil {
		log.Warn("Could not load claude config, using defaults", "error", err)
		cfg = claude.DefaultConfig()
	}

	// Determine the range.
	fromRef := cmd.From
	if fromRef == "" {
		fromRef = store.Trunk()
	}

	toRef := cmd.To
	if toRef == "" {
		currentBranch, err := wt.CurrentBranch(ctx)
		if err != nil {
			return fmt.Errorf("get current branch: %w", err)
		}
		toRef = currentBranch
	}

	title := cmd.Title
	if title == "" {
		if fromRef == store.Trunk() {
			title = toRef
		} else {
			title = fromRef + "..." + toRef
		}
	}

	if cmd.PerBranch {
		return cmd.runPerBranch(ctx, log, view, repo, svc, store, client, cfg, fromRef, toRef, title)
	}

	return cmd.runOverall(ctx, log, view, repo, client, cfg, fromRef, toRef, title)
}

func (cmd *claudeReviewCmd) runOverall(
	ctx context.Context,
	log *silog.Logger,
	view ui.View,
	repo *git.Repository,
	client *claude.Client,
	cfg *claude.Config,
	fromRef, toRef, title string,
) error {
	log.Infof("Reviewing changes: %s...%s", fromRef, toRef)

	// Get the diff text.
	diffText, err := repo.DiffText(ctx, fromRef, toRef)
	if err != nil {
		return fmt.Errorf("get diff: %w", err)
	}

	if diffText == "" {
		log.Info("No changes to review")
		return nil
	}

	// Parse and filter the diff.
	files, err := claude.ParseDiff(diffText)
	if err != nil {
		return fmt.Errorf("parse diff: %w", err)
	}

	filtered := claude.FilterDiff(files, cfg.IgnorePatterns)
	if len(filtered) == 0 {
		log.Info("No changes to review after filtering")
		return nil
	}

	// Check budget.
	budget := claude.CheckBudget(filtered, cfg.MaxLines)
	if budget.OverBudget {
		return cmd.handleOverBudget(view, budget)
	}

	// Reconstruct filtered diff.
	filteredDiff := claude.ReconstructDiff(filtered)

	// Build prompt and run.
	prompt := claude.BuildReviewPrompt(cfg, title, filteredDiff)

	fmt.Fprint(view, "Sending to Claude for review... ")
	response, err := client.RunWithModel(ctx, prompt, cfg.Models.Review)
	fmt.Fprintln(view, "done")
	if err != nil {
		return cmd.handleClaudeError(err)
	}

	// Display the review.
	fmt.Fprintln(view, "")
	fmt.Fprintln(view, "=== Claude Review ===")
	fmt.Fprintln(view, "")
	fmt.Fprintln(view, response)

	// Offer to apply fixes if requested.
	if cmd.Fix && ui.Interactive(view) {
		return cmd.offerFixes(ctx, view, client, cfg, response, filteredDiff)
	}

	return nil
}

func (cmd *claudeReviewCmd) runPerBranch(
	ctx context.Context,
	log *silog.Logger,
	view ui.View,
	repo *git.Repository,
	svc *spice.Service,
	store *state.Store,
	client *claude.Client,
	cfg *claude.Config,
	fromRef, toRef, title string,
) error {
	graph, err := svc.BranchGraph(ctx, nil)
	if err != nil {
		return fmt.Errorf("load branch graph: %w", err)
	}

	branches := collectBranchPath(graph, store.Trunk(), toRef)
	if len(branches) == 0 {
		log.Info("No tracked branches found in range")
		return cmd.runOverall(ctx, log, view, repo, client, cfg, fromRef, toRef, title)
	}

	var reviews []string
	for _, branch := range branches {
		review, err := cmd.reviewSingleBranch(
			ctx, log, view, repo, graph, store, client, cfg, branch,
		)
		if err != nil {
			return err
		}
		if review != "" {
			reviews = append(reviews, review)
		}
	}

	// Generate stack summary if multiple branches were reviewed.
	if len(reviews) > 1 {
		if err := cmd.generateStackSummary(ctx, view, client, cfg, reviews); err != nil {
			return err
		}
	}

	return nil
}

// reviewSingleBranch reviews a single branch and returns the review text.
// Returns empty string if the branch should be skipped.
func (cmd *claudeReviewCmd) reviewSingleBranch(
	ctx context.Context,
	log *silog.Logger,
	view ui.View,
	repo *git.Repository,
	graph *spice.BranchGraph,
	store *state.Store,
	client *claude.Client,
	cfg *claude.Config,
	branch string,
) (string, error) {
	info, ok := graph.Lookup(branch)
	if !ok {
		return "", nil
	}

	base := info.Base
	if base == "" {
		base = store.Trunk()
	}

	log.Infof("Reviewing branch: %s (base: %s)", branch, base)

	diffText, err := repo.DiffText(ctx, base, branch)
	if err != nil {
		log.Warn("Could not get diff for branch", "branch", branch, "error", err)
		return "", nil
	}

	if diffText == "" {
		log.Infof("Branch %s has no changes", branch)
		return "", nil
	}

	files, err := claude.ParseDiff(diffText)
	if err != nil {
		log.Warn("Could not parse diff for branch", "branch", branch, "error", err)
		return "", nil
	}

	filtered := claude.FilterDiff(files, cfg.IgnorePatterns)
	if len(filtered) == 0 {
		log.Infof("Branch %s has no changes after filtering", branch)
		return "", nil
	}

	budget := claude.CheckBudget(filtered, cfg.MaxLines)
	if budget.OverBudget {
		log.Warnf("Branch %s exceeds budget (%d lines > %d max)",
			branch, budget.TotalLines, budget.MaxLines)
		return "", nil
	}

	filteredDiff := claude.ReconstructDiff(filtered)
	prompt := claude.BuildReviewPrompt(cfg, branch, filteredDiff)

	fmt.Fprint(view, "Reviewing... ")
	response, err := client.RunWithModel(ctx, prompt, cfg.Models.Review)
	fmt.Fprintln(view, "done")
	if err != nil {
		return "", cmd.handleClaudeError(err)
	}

	fmt.Fprintln(view, "")
	fmt.Fprintf(view, "=== Review: %s ===\n", branch)
	fmt.Fprintln(view, "")
	fmt.Fprintln(view, response)

	return fmt.Sprintf("## Branch: %s\n\n%s", branch, response), nil
}

// generateStackSummary generates and displays a summary of all branch reviews.
func (cmd *claudeReviewCmd) generateStackSummary(
	ctx context.Context,
	view ui.View,
	client *claude.Client,
	cfg *claude.Config,
	reviews []string,
) error {
	fmt.Fprint(view, "Generating stack summary... ")

	// Build stack summary with separator.
	var summary strings.Builder
	for i, review := range reviews {
		if i > 0 {
			summary.WriteString("\n\n---\n\n")
		}
		summary.WriteString(review)
	}

	prompt := claude.BuildStackReviewPrompt(cfg, summary.String())
	response, err := client.RunWithModel(ctx, prompt, cfg.Models.Review)
	fmt.Fprintln(view, "done")
	if err != nil {
		return cmd.handleClaudeError(err)
	}

	fmt.Fprintln(view, "")
	fmt.Fprintln(view, "=== Stack Summary ===")
	fmt.Fprintln(view, "")
	fmt.Fprintln(view, response)

	return nil
}

func (cmd *claudeReviewCmd) handleOverBudget(view ui.View, budget claude.BudgetResult) error {
	fmt.Fprintf(view, "Diff too large (%d lines, budget: %d)\n", budget.TotalLines, budget.MaxLines)
	fmt.Fprintln(view, "")
	fmt.Fprintln(view, "Options:")
	fmt.Fprintln(view, "  1. Narrow range with --from/--to")
	fmt.Fprintln(view, "  2. Large files:")

	// Sort files by line count (descending).
	type fileEntry struct {
		path  string
		lines int
	}
	var entries []fileEntry
	for path, lines := range budget.FileLines {
		entries = append(entries, fileEntry{path, lines})
	}
	slices.SortFunc(entries, func(a, b fileEntry) int {
		return cmp.Compare(b.lines, a.lines) // descending
	})

	// Show top N largest files.
	const maxFilesToShow = 5
	for i := range min(len(entries), maxFilesToShow) {
		fmt.Fprintf(view, "     - %s (%d lines)\n", entries[i].path, entries[i].lines)
	}

	fmt.Fprintln(view, "")
	fmt.Fprintln(view, "Add patterns to ignorePatterns in:", claude.DefaultConfigPath())

	return errors.New("diff exceeds budget")
}

func (cmd *claudeReviewCmd) handleClaudeError(err error) error {
	switch {
	case errors.Is(err, claude.ErrNotAuthenticated):
		return errors.New("not authenticated with Claude; please run 'claude auth'")
	case errors.Is(err, claude.ErrRateLimited):
		return errors.New("claude rate limit exceeded; please try again later")
	default:
		return fmt.Errorf("claude: %w", err)
	}
}

func (cmd *claudeReviewCmd) offerFixes(
	ctx context.Context,
	view ui.View,
	client *claude.Client,
	cfg *claude.Config,
	review, diff string,
) error {
	fmt.Fprintln(view, "")

	type choice int
	const (
		choiceApply choice = iota
		choiceSkip
	)

	var selected choice
	field := ui.NewSelect[choice]().
		WithTitle("Apply fixes?").
		WithValue(&selected).
		WithOptions(
			ui.SelectOption[choice]{Label: "Apply suggested fixes", Value: choiceApply},
			ui.SelectOption[choice]{Label: "Skip", Value: choiceSkip},
		)

	if err := ui.Run(view, field); err != nil {
		return err
	}

	if selected == choiceSkip {
		return nil
	}

	// Build fix prompt with review context.
	fixPrompt := `Based on the following code review, apply the suggested fixes.
Only modify files that need changes based on the review.
Do not add any new functionality beyond what the review suggests.

## Review:
` + review + `

## Current diff:
` + diff

	fmt.Fprint(view, "Applying fixes with Claude... ")
	response, err := client.RunWithModel(ctx, fixPrompt, cfg.Models.Review)
	fmt.Fprintln(view, "done")
	if err != nil {
		return cmd.handleClaudeError(err)
	}

	fmt.Fprintln(view, "")
	fmt.Fprintln(view, "=== Applied Fixes ===")
	fmt.Fprintln(view, "")
	fmt.Fprintln(view, response)

	return nil
}

// collectBranchPath collects branches from trunk to target in the branch graph.
// Returns branches in bottom-up order (closest to trunk first, target last).
func collectBranchPath(graph *spice.BranchGraph, trunk, target string) []string {
	var branches []string

	current := target
	for current != "" && current != trunk {
		branches = append([]string{current}, branches...)

		info, ok := graph.Lookup(current)
		if !ok {
			break
		}
		current = info.Base
	}

	return branches
}
