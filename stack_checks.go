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

// stackChecksCmd prints CI check summaries for every PR in the stack.
type stackChecksCmd struct {
	IncludePassing bool `help:"Include passing/in_progress checks too"`
}

func (*stackChecksCmd) Help() string {
	return `Iterates every branch in the current stack, checks out each
in turn, and prints the CI check summary for its PR.

The original branch is restored when the command exits. Per-branch
errors are collected and reported at the end rather than aborting
the whole stack walk.
`
}

func (c *stackChecksCmd) Run(
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
	if err := ensureCleanTree(ctx, wt); err != nil {
		return fmt.Errorf("%w; commit or stash before running stack checks", err)
	}

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
		cmd := &branchChecksCmd{
			Branch:         branch,
			IncludePassing: c.IncludePassing,
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
