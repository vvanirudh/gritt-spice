package main

import (
	"context"
	"fmt"

	"go.abhg.dev/gs/internal/git"
	"go.abhg.dev/gs/internal/handler/restack"
	"go.abhg.dev/gs/internal/spice"
	"go.abhg.dev/gs/internal/text"
)

type stackRestackCmd struct {
	Branch string `help:"Branch to restack the stack of" placeholder:"NAME" predictor:"trackedBranches"`
	Method string `config:"restack.method" default:"rebase" help:"Method to use for restacking: 'rebase' or 'merge'" enum:"rebase,merge"`
}

func (*stackRestackCmd) Help() string {
	return text.Dedent(`
		All branches in the current stack are restacked on top of their
		respective bases.
		By default, uses rebase to ensure a linear history.
		Set 'spice.restack.method=merge' to use merge commits instead,
		which preserves individual commit history.

		Use --branch to restack the stack of a different branch.
	`)
}

func (cmd *stackRestackCmd) AfterApply(ctx context.Context, wt *git.Worktree) error {
	if cmd.Branch == "" {
		currentBranch, err := wt.CurrentBranch(ctx)
		if err != nil {
			return fmt.Errorf("get current branch: %w", err)
		}
		cmd.Branch = currentBranch
	}
	return nil
}

func (cmd *stackRestackCmd) Run(ctx context.Context, handler RestackHandler) error {
	// Parse the restack method from configuration
	method, err := spice.ParseRestackMethod(cmd.Method)
	if err != nil {
		return fmt.Errorf("invalid restack method: %w", err)
	}

	// Configure the handler with the restack method if it's a restack.Handler
	if h, ok := handler.(*restack.Handler); ok {
		handler = h.WithRestackMethod(method)
	}

	return handler.RestackStack(ctx, cmd.Branch)
}
