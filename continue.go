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
		a conflict during rebase or merge after all conflicts have been resolved.
		
		This command automatically detects whether the interruption was caused by
		a rebase or merge operation and continues appropriately.
		
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
	// Check if there's a rebase in progress
	if _, err := wt.RebaseState(ctx); err == nil {
		// Rebase is in progress, delegate to rebase continue
		log.Debug("Rebase in progress, delegating to 'gs rebase continue'")
		kctx, err := parser.Parse([]string{"rebase", "continue"})
		if err != nil {
			return fmt.Errorf("parse rebase continue command: %w", err)
		}
		return kctx.Run(ctx)
	}

	// Check if there's a merge in progress
	if _, mergeStateErr := wt.MergeState(ctx); mergeStateErr == nil {
		// Merge is in progress, handle merge continue
		log.Debug("Merge in progress, continuing merge")
		
		var opts git.MergeContinueOptions
		if !cmd.Edit {
			opts.Editor = "true"
		}

		if err := wt.MergeContinue(ctx, &opts); err != nil {
			var mergeErr *git.MergeInterruptError
			if errors.As(err, &mergeErr) {
				log.Error("There are more conflicts to resolve.\n" +
					"Resolve them and run the following command again:\n" +
					"  gs continue\n" +
					"To abort the remaining operations run:\n" +
					"  gs abort\n")
			}
			return err
		}

		// After completing the merge, check if this was a restack merge and update the branch pointer
		headCommit, err := wt.Repository().PeelToCommit(ctx, "HEAD")
		if err != nil {
			return fmt.Errorf("get HEAD commit after merge: %w", err)
		}

		subject, err := wt.Repository().CommitSubject(ctx, headCommit.String())
		if err != nil {
			return fmt.Errorf("get commit subject: %w", err)
		}

		// Check if this is a restack merge commit and update the branch pointer
		if strings.Contains(subject, "Restack ") && strings.Contains(subject, " via merge") {
			log.Debug("Detected restack merge completion", "commit", headCommit, "message", subject)
			
			// Extract branch name from commit message: "Restack <branch> onto <base> via merge"
			parts := strings.Split(subject, " ")
			if len(parts) >= 4 && parts[0] == "Restack" && parts[2] == "onto" {
				branchName := parts[1]
				log.Debug("Updating branch pointer after restack merge", "branch", branchName, "commit", headCommit)
				
				// Update the branch to point to the merge commit
				if err := wt.Repository().CreateBranch(ctx, git.CreateBranchRequest{
					Name:  branchName,
					Head:  headCommit.String(),
					Force: true,
				}); err != nil {
					log.Warn("Failed to update branch pointer after restack merge", "branch", branchName, "error", err)
				} else {
					log.Debug("Successfully updated branch pointer after restack merge", "branch", branchName)
				}
			}
		}

		// Continue with any queued continuations (for non-restack merges or if restack detection failed)
		conts, err := store.TakeContinuations(ctx, "gs continue")
		if err != nil {
			return fmt.Errorf("take continuations: %w", err)
		}

		for idx, cont := range conts {
			log.Debug("Running post-merge operation",
				"command", fmt.Sprintf("%v", cont.Command),
				"branch", cont.Branch)
			
			if err := wt.Checkout(ctx, cont.Branch); err != nil {
				return fmt.Errorf("checkout branch %q: %w", cont.Branch, err)
			}

			kctx, err := parser.Parse(cont.Command)
			if err != nil {
				log.Errorf("Corrupt continuation: %q", cont.Command)
				return fmt.Errorf("parse continuation: %w", err)
			}

			if err := kctx.Run(ctx); err != nil {
				// If the command failed, append the remainder
				if err := store.AppendContinuations(ctx, "continue", conts[idx+1:]...); err != nil {
					return fmt.Errorf("append continuations: %w", err)
				}
				return err
			}
		}

		return nil
	}

	// Neither rebase nor merge in progress
	return errors.New("no rebase or merge in progress")
}