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

// stackReviewsCmd walks review threads for every PR in the stack.
type stackReviewsCmd struct {
	Batch              bool `help:"Pass through to each branch's reviews command"`
	NoDescriptionCheck bool `help:"Skip aggregated description-accuracy check (reserved)"`
	Concurrency        int  `help:"Parallel classifications" default:"4"`
}

func (*stackReviewsCmd) Help() string {
	return `Iterates every branch in the current stack,
checks out each one in turn, and runs 'branch reviews' on its PR.

The original branch is restored when the command exits.
Per-branch errors are collected and reported at the end
rather than aborting the whole stack walk.
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
	// Refuse on dirty working tree.
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

	// Walk each non-trunk branch.
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

		cmd := &branchReviewsCmd{
			Branch:      branch,
			Batch:       c.Batch,
			Concurrency: c.Concurrency,
		}
		if err := cmd.Run(ctx, log, view, wt, repo, store, stash, forges); err != nil {
			errs = append(errs, branchErr{branch, err})
		}
	}

	// Report per-branch errors.
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

// ensureCleanTree returns an error if the working tree has uncommitted changes.
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

	return nil
}
