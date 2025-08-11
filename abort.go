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
		// Merge is in progress, abort it
		log.Debug("Merge in progress, aborting merge")
		if err := wt.MergeAbort(ctx); err != nil {
			return fmt.Errorf("abort merge: %w", err)
		}
		log.Info("Merge aborted.")
		
		// Clear any continuations
		if conts, err := store.TakeContinuations(ctx, "abort merge"); err != nil {
			log.Warn("Failed to clear continuations", "error", err)
		} else if len(conts) > 0 {
			log.Infof("Cleared %d queued continuation(s).", len(conts))
		}
		
		return nil
	}

	// Neither rebase nor merge in progress
	return errors.New("no rebase or merge in progress")
}