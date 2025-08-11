package main

import (
	"context"
	"fmt"

	"go.abhg.dev/gs/internal/git"
	"go.abhg.dev/gs/internal/handler/restack"
	"go.abhg.dev/gs/internal/silog"
	"go.abhg.dev/gs/internal/spice"
	"go.abhg.dev/gs/internal/spice/state"
	"go.abhg.dev/gs/internal/text"
)

type repoRestackCmd struct {
	Method string `config:"restack.method" default:"rebase" help:"Method to use for restacking: 'rebase' or 'merge'" enum:"rebase,merge"`
}

func (*repoRestackCmd) Help() string {
	return text.Dedent(`
		All tracked branches in the repository are rebased on top of their
		respective bases in dependency order, ensuring a linear history.
	`)
}

func (cmd *repoRestackCmd) Run(
	ctx context.Context,
	log *silog.Logger,
	wt *git.Worktree,
	store *state.Store,
	handler RestackHandler,
) error {
	currentBranch, err := wt.CurrentBranch(ctx)
	if err != nil {
		return fmt.Errorf("get current branch: %w", err)
	}

	// Parse the restack method
	method, err := spice.ParseRestackMethod(cmd.Method)
	if err != nil {
		return fmt.Errorf("invalid restack method: %w", err)
	}

	// Update the handler with the configured method
	if h, ok := handler.(*restack.Handler); ok {
		handler = h.WithRestackMethod(method)
	}

	count, err := handler.Restack(ctx, &restack.Request{
		Branch:          store.Trunk(),
		Scope:           restack.ScopeUpstackExclusive,
		ContinueCommand: []string{"repo", "restack"},
	})
	if err != nil {
		return err
	}

	if count == 0 {
		log.Infof("Nothing to restack: no tracked branches available")
		return nil
	}

	if err := wt.Checkout(ctx, currentBranch); err != nil {
		return fmt.Errorf("checkout %v: %w", currentBranch, err)
	}

	log.Infof("Restacked %d branches", count)
	return nil
}
