package main

import (
	"context"
	"errors"
	"fmt"

	"go.abhg.dev/gs/internal/git"
	"go.abhg.dev/gs/internal/handler/restack"
	"go.abhg.dev/gs/internal/silog"
	"go.abhg.dev/gs/internal/spice"
	"go.abhg.dev/gs/internal/text"
)

type commitCreateCmd struct {
	All        bool   `short:"a" help:"Stage all changes before committing."`
	AllowEmpty bool   `help:"Create a new commit even if it contains no changes."`
	Fixup      string `help:"Create a fixup commit."`
	Message    string `short:"m" help:"Use the given message as the commit message."`
	NoVerify   bool   `help:"Bypass pre-commit and commit-msg hooks."`
	Method     string `config:"restack.method" default:"rebase" help:"Method to use for restacking: 'rebase' or 'merge'" enum:"rebase,merge"`
}

func (*commitCreateCmd) Help() string {
	return text.Dedent(`
		Staged changes are committed to the current branch.
		Branches upstack are restacked if necessary.
		By default, uses rebase to ensure a linear history.
		Set 'spice.restack.method=merge' to use merge commits instead,
		which preserves individual commit history.
		Use this as a shortcut for 'git commit'
		followed by 'gs upstack restack'.
	`)
}

func (cmd *commitCreateCmd) Run(
	ctx context.Context,
	log *silog.Logger,
	wt *git.Worktree,
	restackHandler RestackHandler,
) error {
	if err := wt.Commit(ctx, git.CommitRequest{
		Message:    cmd.Message,
		All:        cmd.All,
		AllowEmpty: cmd.AllowEmpty,
		Fixup:      cmd.Fixup,
		NoVerify:   cmd.NoVerify,
	}); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	if _, err := wt.RebaseState(ctx); err == nil {
		// In the middle of a rebase.
		// Don't restack upstack branches.
		log.Debug("A rebase is in progress, skipping restack")
		return nil
	}

	currentBranch, err := wt.CurrentBranch(ctx)
	if err != nil {
		// No restack needed if we're in a detached head state.
		if errors.Is(err, git.ErrDetachedHead) {
			log.Debug("HEAD is detached, skipping restack")
			return nil
		}
		return fmt.Errorf("get current branch: %w", err)
	}

	// Parse the restack method from configuration
	method, err := spice.ParseRestackMethod(cmd.Method)
	if err != nil {
		return fmt.Errorf("invalid restack method: %w", err)
	}

	// Configure the handler with the restack method if it's a restack.Handler
	if h, ok := restackHandler.(*restack.Handler); ok {
		restackHandler = h.WithRestackMethod(method)
	}

	return restackHandler.RestackUpstack(ctx, currentBranch, &restack.UpstackOptions{
		SkipStart: true,
	})
}
