package main

import (
	"context"
	"errors"
	"fmt"

	"go.abhg.dev/gs/internal/git"
	"go.abhg.dev/gs/internal/silog"
	"go.abhg.dev/gs/internal/spice/state"
	"go.abhg.dev/gs/internal/text"
)

type abortCmd struct{}

func (*abortCmd) Help() string {
	return text.Dedent(`
		Aborts an ongoing git-spice operation that was interrupted by
		a conflict during rebase or merge.
		
		This command automatically detects whether the interruption was caused by
		a rebase or merge operation and aborts appropriately.
		
		Any queued continuations from the interrupted operation will be cleared.
	`)
}

func (cmd *abortCmd) Run(
	ctx context.Context,
	log *silog.Logger,
	wt *git.Worktree,
	store *state.Store,
) error {
	// Check if there's a rebase in progress
	if _, err := wt.RebaseState(ctx); err == nil {
		// Rebase is in progress, abort it
		log.Debug("Rebase in progress, aborting rebase")
		if err := wt.RebaseAbort(ctx); err != nil {
			return fmt.Errorf("abort rebase: %w", err)
		}
		log.Info("Rebase aborted.")
		
		// Clear any continuations
		if conts, err := store.TakeContinuations(ctx, "abort rebase"); err != nil {
			log.Warn("Failed to clear continuations", "error", err)
		} else if len(conts) > 0 {
			log.Infof("Cleared %d queued continuation(s).", len(conts))
		}
		
		return nil
	}

	// Check if there's a merge in progress
	if _, err := wt.MergeState(ctx); err == nil {
		// Before aborting, get continuation information to determine what branch to restore
		// This is needed for merge-based restacking which may leave us in detached HEAD
		conts, contErr := store.TakeContinuations(ctx, "abort merge")
		var targetBranch string
		if contErr == nil && len(conts) > 0 {
			targetBranch = conts[0].Branch
			if targetBranch != "" {
				log.Debug("Found target branch from continuations", "branch", targetBranch)
			}
		}

		// Merge is in progress, abort it
		log.Debug("Merge in progress, aborting merge")
		if err := wt.MergeAbort(ctx); err != nil {
			// If abort fails and we have continuations, try to restore them
			if contErr == nil && len(conts) > 0 {
				if restoreErr := store.AppendContinuations(ctx, "restore after failed abort", conts...); restoreErr != nil {
					log.Warn("Failed to restore continuations after failed abort", "error", restoreErr)
				}
			}
			return fmt.Errorf("abort merge: %w", err)
		}
		log.Info("Merge aborted.")
		
		// After successful abort, try to checkout the target branch if we have one
		if targetBranch != "" {
			if err := wt.Checkout(ctx, targetBranch); err != nil {
				log.Warn("Could not checkout target branch after abort", 
					"branch", targetBranch, "error", err)
			} else {
				log.Debug("Restored target branch after abort", "branch", targetBranch)
			}
		}
		
		// Report cleared continuations
		if contErr != nil {
			log.Warn("Failed to clear continuations", "error", contErr)
		} else if len(conts) > 0 {
			log.Infof("Cleared %d queued continuation(s).", len(conts))
		}
		
		return nil
	}

	// Neither rebase nor merge in progress
	return errors.New("no rebase or merge in progress")
}