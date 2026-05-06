package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"go.abhg.dev/gs/internal/forge"
	"go.abhg.dev/gs/internal/git"
	"go.abhg.dev/gs/internal/secret"
	"go.abhg.dev/gs/internal/silog"
	"go.abhg.dev/gs/internal/spice/state"
	"go.abhg.dev/gs/internal/ui"
)

// branchChecksCmd fetches CI check runs for a branch's PR and prints
// a summary. Read-only by design — fixing failing checks is the
// user's job (push more commits, debug locally, etc.).
type branchChecksCmd struct {
	Branch         string `help:"Branch to fetch checks for (defaults to current)" predictor:"trackedBranches"`
	IncludePassing bool   `help:"Include passing/in_progress checks too (default: only failing)"`
}

func (*branchChecksCmd) Help() string {
	return `Fetches CI check runs for the current branch's pull request and
prints a summary table.

By default only failing checks are shown. Use --include-passing to
include passing and in-progress checks too.

This command is read-only; fixing failing checks is the user's job.
`
}

func (c *branchChecksCmd) Run(
	ctx context.Context,
	log *silog.Logger,
	view ui.View,
	wt *git.Worktree,
	repo *git.Repository,
	store *state.Store,
	stash secret.Stash,
	forges *forge.Registry,
) error {
	if c.Branch == "" {
		currentBranch, err := wt.CurrentBranch(ctx)
		if err != nil {
			return fmt.Errorf("get current branch: %w", err)
		}
		c.Branch = currentBranch
	}

	remote, err := ensureRemote(ctx, repo, store, log, view)
	if err != nil {
		return fmt.Errorf("get remote: %w", err)
	}
	forgeRepo, err := openRemoteRepository(ctx, log, stash, forges, repo, remote)
	if err != nil {
		return fmt.Errorf("open remote repository: %w", err)
	}

	checker, ok := forgeRepo.(forge.ChangeChecksLister)
	if !ok {
		return fmt.Errorf(
			"forge %q does not support CI check listing",
			forgeRepo.Forge().ID(),
		)
	}

	changes, err := forgeRepo.FindChangesByBranch(
		ctx, c.Branch, forge.FindChangesOptions{Limit: 1},
	)
	if err != nil {
		return fmt.Errorf("find changes for branch %q: %w", c.Branch, err)
	}
	if len(changes) == 0 {
		return fmt.Errorf("no open pull request found for branch %q", c.Branch)
	}
	prID := changes[0].ID

	var checks []*forge.ChangeCheckItem
	for it, err := range checker.ListChangeChecks(
		ctx, prID,
		&forge.ListChangeChecksOptions{OnlyFailing: !c.IncludePassing},
	) {
		if err != nil {
			return fmt.Errorf("list change checks: %w", err)
		}
		checks = append(checks, it)
	}

	if len(checks) == 0 {
		if c.IncludePassing {
			fmt.Fprintln(view, "no checks found")
		} else {
			fmt.Fprintln(view, "no failing checks")
		}
		return nil
	}

	printChecksSummary(view, checks)
	return nil
}

// printChecksSummary writes a one-line-per-check summary, grouped by
// conclusion (failing items first, then passing/in-progress).
func printChecksSummary(out io.Writer, checks []*forge.ChangeCheckItem) {
	fmt.Fprintf(out, "\n%d check(s):\n\n", len(checks))
	for _, c := range checks {
		conclusion := c.Conclusion
		if conclusion == "" {
			conclusion = strings.ToLower(c.Status)
		}
		marker := "·"
		switch conclusion {
		case "failure", "timed_out", "cancelled", "action_required":
			marker = "✗"
		case "success":
			marker = "✓"
		}
		fmt.Fprintf(out, "  %s [%s] %s\n", marker, conclusion, c.Name)
		if c.URL != "" {
			fmt.Fprintf(out, "       %s\n", c.URL)
		}
	}
	fmt.Fprintln(out)
}
