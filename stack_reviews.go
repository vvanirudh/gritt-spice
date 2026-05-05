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
	return `Iterates every branch in the current stack, checks out each
in turn, and prints the per-file review-thread summary for its PR.

The original branch is restored when the command exits. Per-branch
errors are collected and reported at the end rather than aborting
the whole stack walk.

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
	// Refuse on dirty working tree (we'll be checking out branches).
	if err := ensureCleanTree(ctx, wt); err != nil {
		return fmt.Errorf("%w; commit or stash before running stack reviews", err)
	}

	// Record current branch for restoration.
	original, err := wt.CurrentBranch(ctx)
	if err != nil {
		return fmt.Errorf("get current branch: %w", err)
	}
	defer func() {
		if restoreErr := wt.CheckoutBranch(ctx, original); restoreErr != nil {
			log.Warn("Could not restore original branch",
				"branch", original, "error", restoreErr)
		}
	}()

	// List branches in the stack (trunk-to-tip order).
	stack, err := svc.ListStack(ctx, original)
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

		if err := wt.CheckoutBranch(ctx, branch); err != nil {
			errs = append(errs, branchErr{branch, err})
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

// ensureCleanTree returns an error if the working tree has any local
// state that should be committed or stashed before checking out
// another branch: staged changes, unstaged changes, or untracked
// files. Untracked files are included because they're easy for the
// user to lose track of when bouncing branches and the comment
// "uncommitted changes" implies them anyway.
func ensureCleanTree(ctx context.Context, wt *git.Worktree) error {
	staged, err := wt.DiffIndex(ctx, "HEAD")
	if err != nil {
		return fmt.Errorf("check staged changes: %w", err)
	}
	if len(staged) > 0 {
		return errors.New("working tree has uncommitted changes")
	}

	var hasUnstaged bool
	for _, err := range wt.DiffWork(ctx) {
		if err != nil {
			return fmt.Errorf("check unstaged changes: %w", err)
		}
		hasUnstaged = true
		break
	}
	if hasUnstaged {
		return errors.New("working tree has uncommitted changes")
	}

	for path, err := range wt.ListUntrackedFiles(ctx) {
		if err != nil {
			return fmt.Errorf("check untracked files: %w", err)
		}
		return fmt.Errorf(
			"working tree has untracked files (e.g. %s)", path,
		)
	}
	return nil
}
