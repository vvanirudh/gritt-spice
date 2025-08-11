package main

import (
	"context"
	"fmt"

	"go.abhg.dev/gs/internal/git"
	"go.abhg.dev/gs/internal/handler/restack"
	"go.abhg.dev/gs/internal/spice"
	"go.abhg.dev/gs/internal/text"
)

type upstackRestackCmd struct {
	restack.UpstackOptions

	Branch string `help:"Branch to restack the upstack of" placeholder:"NAME" predictor:"trackedBranches"`
	Method string `config:"restack.method" default:"rebase" help:"Method to use for restacking: 'rebase' or 'merge'" enum:"rebase,merge"`
}

func (*upstackRestackCmd) Help() string {
	return text.Dedent(`
		The current branch and all branches above it
		are restacked on top of their respective bases.
		By default, uses rebase to ensure a linear history.
		Set 'spice.restack.method=merge' to use merge commits instead,
		which preserves individual commit history.
		Use --branch to start at a different branch.
		Use --skip-start to skip the starting branch,
		but still restack all branches above it.

		The target branch defaults to the current branch.
		If run from the trunk branch,
		all managed branches will be restacked.
	`)
}

// RestackHandler implements high level restack operations.
type RestackHandler interface {
	RestackUpstack(ctx context.Context, branch string, opts *restack.UpstackOptions) error
	Restack(context.Context, *restack.Request) (int, error)
	RestackStack(ctx context.Context, branch string) error
	RestackBranch(ctx context.Context, branch string) error
}

func (cmd *upstackRestackCmd) AfterApply(ctx context.Context, wt *git.Worktree) error {
	if cmd.Branch == "" {
		currentBranch, err := wt.CurrentBranch(ctx)
		if err != nil {
			return fmt.Errorf("get current branch: %w", err)
		}
		cmd.Branch = currentBranch
	}
	return nil
}

func (cmd *upstackRestackCmd) Run(ctx context.Context, handler RestackHandler) error {
	// Parse the restack method from configuration
	method, err := spice.ParseRestackMethod(cmd.Method)
	if err != nil {
		return fmt.Errorf("invalid restack method: %w", err)
	}

	// Configure the handler with the restack method if it's a restack.Handler
	if h, ok := handler.(*restack.Handler); ok {
		handler = h.WithRestackMethod(method)
	}

	return handler.RestackUpstack(ctx, cmd.Branch, &cmd.UpstackOptions)
}
