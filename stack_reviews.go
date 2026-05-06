package main

import (
	"context"
	"errors"
	"fmt"

	"go.abhg.dev/gs/internal/forge"
	"go.abhg.dev/gs/internal/git"
	"go.abhg.dev/gs/internal/secret"
	"go.abhg.dev/gs/internal/silog"
	"go.abhg.dev/gs/internal/spice"
	"go.abhg.dev/gs/internal/spice/state"
	"go.abhg.dev/gs/internal/ui"
)

// stackReviewsCmd prints review-thread summaries for every PR in the
// stack. Read-only; mirrors `branch reviews` semantics across all
// branches in the current stack.
type stackReviewsCmd struct {
	IncludeResolved bool   `help:"Include resolved threads"`
	BotAllowlist    string `help:"Comma-separated bot logins to include" default:"copilot,claude,codex,github-advanced-security,copilot-pull-request-reviewer"`
}

func (*stackReviewsCmd) Help() string {
	return `Iterates every branch in the current stack and prints the
per-file review-thread summary for its PR.

This is a forge-API operation: branches are not checked out and the
working tree is not touched, so it works with uncommitted changes and
with branches that are checked out in other worktrees.

Per-branch errors are collected and reported at the end rather than
aborting the whole stack walk.

This command is read-only; addressing comments is the user's job.
`
}

func (c *stackReviewsCmd) Run(
	ctx context.Context,
	log *silog.Logger,
	view ui.View,
	wt *git.Worktree,
	repo *git.Repository,
	store *state.Store,
	stash secret.Stash,
	forges *forge.Registry,
	svc *spice.Service,
) error {
	// We need the current branch to anchor the stack walk; we don't
	// touch the working tree beyond that.
	current, err := wt.CurrentBranch(ctx)
	if err != nil {
		return fmt.Errorf("get current branch: %w", err)
	}

	// List branches in the stack (trunk-to-tip order).
	stack, err := svc.ListStack(ctx, current)
	if err != nil {
		return fmt.Errorf("list stack: %w", err)
	}

	type branchErr struct {
		branch string
		err    error
	}
	var errs []branchErr
	for _, branch := range stack {
		if branch == store.Trunk() {
			continue
		}

		fmt.Fprintf(view, "\n=== %s ===\n", branch)
		cmd := &branchReviewsCmd{
			Branch:          branch,
			IncludeResolved: c.IncludeResolved,
			BotAllowlist:    c.BotAllowlist,
		}
		if err := cmd.Run(ctx, log, view, wt, repo, store, stash, forges); err != nil {
			errs = append(errs, branchErr{branch, err})
		}
	}

	if len(errs) > 0 {
		fmt.Fprintln(view, "")
		fmt.Fprintln(view, "Errors encountered:")
		for _, be := range errs {
			fmt.Fprintf(view, "  %s: %v\n", be.branch, be.err)
		}
		return errors.New("one or more branches had errors")
	}
	return nil
}
