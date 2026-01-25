package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.abhg.dev/gs/internal/git"
	"go.abhg.dev/gs/internal/silog"
	"go.abhg.dev/gs/internal/spice/state"
	"go.abhg.dev/gs/internal/text"
)

type abortCmd struct{}

func (*abortCmd) Help() string {
	return text.Dedent(`
		Cancels an ongoing git-spice operation that was interrupted by
		a git rebase or git merge.
		For example, if 'gs upstack restack' encounters a conflict,
		cancel the operation with 'gs abort',
		going back to the state before the operation.

		The command works with both rebase and merge operations,
		automatically detecting which one is in progress.
	`)
}

func (cmd *abortCmd) Run(
	ctx context.Context,
	wt *git.Worktree,
	log *silog.Logger,
	store *state.Store,
) error {
	var wasOperating bool

	// Check for rebase
	if _, err := wt.RebaseState(ctx); err == nil {
		log.Debug("Detected rebase in progress, aborting rebase")
		wasOperating = true
		if err := wt.RebaseAbort(ctx); err != nil {
			return fmt.Errorf("abort rebase: %w", err)
		}
	}

	// Check for merge
	if _, err := wt.MergeState(ctx); err == nil {
		log.Debug("Detected merge in progress, aborting merge")
		wasOperating = true
		if err := wt.MergeAbort(ctx); err != nil {
			return fmt.Errorf("abort merge: %w", err)
		}
	}

	// Clear continuations
	conts, err := store.TakeContinuations(ctx, "gs abort")
	if err != nil {
		return fmt.Errorf("take continuations: %w", err)
	}

	// Make sure that *something* happened from the user's perspective.
	// If we didn't abort an operation, and we didn't delete a continuation,
	// then this was a no-op, which this command should not be.
	if len(conts) == 0 && !wasOperating {
		return errors.New("no operation to abort")
	}

	for _, cont := range conts {
		log.Debug("Operation aborted: will not run command",
			"command", strings.Join(cont.Command, " "),
			"branch", cont.Branch)
	}

	return nil
}
