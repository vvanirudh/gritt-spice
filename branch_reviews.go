package main

import (
	"context"
	"fmt"
	"strings"

	"go.abhg.dev/gs/internal/forge"
	"go.abhg.dev/gs/internal/git"
	"go.abhg.dev/gs/internal/review"
	"go.abhg.dev/gs/internal/secret"
	"go.abhg.dev/gs/internal/silog"
	"go.abhg.dev/gs/internal/spice/state"
	"go.abhg.dev/gs/internal/ui"
)

// branchReviewsCmd fetches review threads for a branch's PR and
// prints a per-file summary. Read-only by design — addressing the
// comments is the user's job (open them in your editor / IDE / claude
// session of choice). Keeping this command informational keeps it
// fast, predictable, and free of agent-permission surprises.
type branchReviewsCmd struct {
	Branch string `help:"Branch to fetch reviews for (defaults to current)" predictor:"trackedBranches"`

	IncludeResolved bool   `help:"Include resolved threads"`
	BotAllowlist    string `help:"Comma-separated bot logins to include" default:"copilot,claude,codex,github-advanced-security,copilot-pull-request-reviewer"`
}

func (*branchReviewsCmd) Help() string {
	return `Fetches open review threads for the current branch's pull request
and prints a per-file summary table.

This command is read-only. It does not classify, address, or reply
to any threads — addressing review comments is the user's job. Use
the printed summary to decide what to work on, then open the items
in your editor / IDE / claude session of your choice.

Threads where you've already replied with the "Addressed in <sha>:
<subject>" marker are filtered out automatically. Resolved threads
are also filtered unless --include-resolved is set.

Bot comments are filtered to a small allowlist by default (Copilot,
Claude, Codex, GitHub Advanced Security). Override with
--bot-allowlist=name1,name2 (empty allowlist excludes all bots).
`
}

func (c *branchReviewsCmd) Run(
	ctx context.Context,
	log *silog.Logger,
	view ui.View,
	wt *git.Worktree,
	repo *git.Repository,
	store *state.Store,
	stash secret.Stash,
	forges *forge.Registry,
) error {
	// Resolve branch name.
	if c.Branch == "" {
		currentBranch, err := wt.CurrentBranch(ctx)
		if err != nil {
			return fmt.Errorf("get current branch: %w", err)
		}
		c.Branch = currentBranch
	}

	// Open the forge repository.
	remote, err := ensureRemote(ctx, repo, store, log, view)
	if err != nil {
		return fmt.Errorf("get remote: %w", err)
	}
	forgeRepo, err := openRemoteRepository(ctx, log, stash, forges, repo, remote)
	if err != nil {
		return fmt.Errorf("open remote repository: %w", err)
	}

	// Capability check: forge must support review threads.
	threader, ok := forgeRepo.(forge.ReviewThreadLister)
	if !ok {
		return fmt.Errorf(
			"forge %q does not support review thread listing",
			forgeRepo.Forge().ID(),
		)
	}

	// Find the PR for this branch.
	changes, err := forgeRepo.FindChangesByBranch(
		ctx, c.Branch, forge.FindChangesOptions{Limit: 1},
	)
	if err != nil {
		return fmt.Errorf("find changes for branch %q: %w", c.Branch, err)
	}
	if len(changes) == 0 {
		return fmt.Errorf("no open pull request found for branch %q", c.Branch)
	}
	prID := changes[0].ID

	// Get viewer login for "already addressed" detection.
	var viewerLogin string
	if vi, ok := forgeRepo.(forge.ViewerIdentifier); ok {
		login, err := vi.ViewerLogin(ctx)
		if err != nil {
			log.Warn(
				"Could not get viewer login; skipping already-addressed check",
				"error", err,
			)
		} else {
			viewerLogin = login
		}
	} else {
		log.Warn("Forge does not support viewer identification; " +
			"skipping already-addressed check")
	}

	// Fetch all review threads.
	var threads []*forge.ReviewThreadItem
	for thread, err := range threader.ListReviewThreads(
		ctx, prID,
		&forge.ListReviewThreadsOptions{
			IncludeResolved: c.IncludeResolved,
			BotAllowlist:    parseCSV(c.BotAllowlist),
		},
	) {
		if err != nil {
			return fmt.Errorf("list review threads: %w", err)
		}
		threads = append(threads, thread)
	}

	// Filter out already-addressed threads. No deferred-state file
	// here — the command is read-only, there's no "defer" action to
	// record. Pass nil deferred so PipelineForThreads only filters
	// based on viewer's posted replies.
	items, _ := review.PipelineForThreads(
		ctx, threads, nil, viewerLogin,
		nil, // no classifier — informational mode is fast by design
		1,
	)

	if len(items) == 0 {
		fmt.Fprintln(view, "no open review threads")
		return nil
	}

	review.PrintSummary(view, items)
	return nil
}

// parseCSV splits a comma-separated string into trimmed, non-empty tokens.
func parseCSV(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
