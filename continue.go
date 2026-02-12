package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/alecthomas/kong"
	"go.abhg.dev/gs/internal/git"
	"go.abhg.dev/gs/internal/silog"
	"go.abhg.dev/gs/internal/spice/state"
	"go.abhg.dev/gs/internal/text"
)

type continueCmd struct {
	Edit bool `default:"true" negatable:"" config:"continue.edit" help:"Whether to open an editor to edit the commit message."`
}

func (*continueCmd) Help() string {
	return text.Dedent(`
		Continues an ongoing git-spice operation interrupted by
		a git rebase or git merge after all conflicts have been resolved.
		For example, if 'gs upstack restack' gets interrupted
		because a conflict arises during the restack,
		you can resolve the conflict and run 'gs continue'
		to continue the operation.

		The command works with both rebase and merge operations,
		automatically detecting which one is in progress.

		Use the --no-edit flag to continue without opening an editor.
		Make --no-edit the default by setting 'spice.continue.edit' to false
		and use --edit to override it.
	`)
}

func (cmd *continueCmd) Run(
	ctx context.Context,
	log *silog.Logger,
	wt *git.Worktree,
	store *state.Store,
	parser *kong.Kong,
) error {
	// Check which operation is in progress
	if _, err := wt.RebaseState(ctx); err == nil {
		// Rebase in progress - delegate to existing logic
		log.Debug("Detected rebase in progress, continuing rebase")
		return (&rebaseContinueCmd{Edit: cmd.Edit}).Run(ctx, log, wt, store, parser)
	}

	if _, err := wt.MergeState(ctx); err == nil {
		// Merge in progress - handle merge continue
		log.Debug("Detected merge in progress, continuing merge")
		return cmd.handleMergeContinue(ctx, log, wt, store, parser)
	}

	return errors.New("no operation in progress")
}

func (cmd *continueCmd) handleMergeContinue(
	ctx context.Context,
	log *silog.Logger,
	wt *git.Worktree,
	store *state.Store,
	parser *kong.Kong,
) error {
	// Finish the merge
	if err := wt.MergeContinue(ctx); err != nil {
		var mergeErr *git.MergeInterruptError
		if errors.As(err, &mergeErr) {
			var msg strings.Builder
			fmt.Fprintf(&msg, "There are more conflicts to resolve.\n")
			fmt.Fprintf(&msg, "Resolve them and run the following command again:\n")
			fmt.Fprintf(&msg, "  gs continue\n")
			fmt.Fprintf(&msg, "To abort the remaining operations run:\n")
			fmt.Fprintf(&msg, "  gs abort\n")
			log.Error(msg.String())
		}
		return err
	}

	// Once we get here, we have a clean state to continue running
	// continuations.
	// However, if any of the continuations encounters another conflict,
	// they will clear the continuation list.
	// So we'll want to grab the whole list here,
	// and push the remainder of it back on if a command fails.
	conts, err := store.TakeContinuations(ctx, "gs continue")
	if err != nil {
		return fmt.Errorf("take continuations: %w", err)
	}

	for idx, cont := range conts {
		log.Debug("Running post-merge operation",
			"command", strings.Join(cont.Command, " "),
			"branch", cont.Branch)
		if err := wt.CheckoutBranch(ctx, cont.Branch); err != nil {
			return fmt.Errorf("checkout branch %q: %w", cont.Branch, err)
		}

		kctx, err := parser.Parse(cont.Command)
		if err != nil {
			log.Errorf("Corrupt continuation: %q", cont.Command)
			return fmt.Errorf("parse continuation: %w", err)
		}

		if err := kctx.Run(ctx); err != nil {
			// If the command failed, it has already printed the
			// message, and appended its continuations.
			// We'll append the remainder.
			if err := store.AppendContinuations(ctx, "continue", conts[idx+1:]...); err != nil {
				return fmt.Errorf("append continuations: %w", err)
			}
			return err
		}
	}

	return nil
}
