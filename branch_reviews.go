package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.abhg.dev/gs/internal/claude"
	"go.abhg.dev/gs/internal/claude/plugin"
	"go.abhg.dev/gs/internal/forge"
	"go.abhg.dev/gs/internal/git"
	"go.abhg.dev/gs/internal/review"
	"go.abhg.dev/gs/internal/secret"
	"go.abhg.dev/gs/internal/silog"
	"go.abhg.dev/gs/internal/spice"
	"go.abhg.dev/gs/internal/spice/state"
	"go.abhg.dev/gs/internal/ui"
)

// branchReviewsCmd fetches review threads for a branch's PR
// and walks through them interactively (or in batch) with Claude AI.
type branchReviewsCmd struct {
	Branch string `help:"Branch to fetch reviews for (defaults to current)" predictor:"trackedBranches"`
	Batch  bool   `help:"Run all items in one Claude session"`

	IncludeResolved bool   `help:"Include resolved threads"`
	BotAllowlist    string `help:"Comma-separated bot logins to include" default:"copilot,claude,codex,github-advanced-security"`
	ResetDeferred   bool   `help:"Clear .git/spice/address-deferred before running"`
	Concurrency     int    `help:"Parallel classifications" default:"4"`
}

func (*branchReviewsCmd) Help() string {
	return `Fetches open review threads for the current branch's pull request,
classifies them with Claude AI, and walks through them one by one.

For each thread, you can choose to:
  - address: spawn a Claude session to make commits addressing the feedback
  - reply: post a manual reply to the thread
  - skip: move to the next thread without action
  - defer: record the thread for later (persisted across runs)

Use --batch to address all threads in a single Claude session instead.
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
	svc *spice.Service,
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
			log.Warn("Could not get viewer login; skipping already-addressed check",
				"error", err)
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

	// Load and reconcile deferred threads.
	deferredPath := filepath.Join(
		wt.RootDir(), ".git", "spice", "address-deferred",
	)
	if c.ResetDeferred {
		if err := os.Remove(deferredPath); err != nil && !os.IsNotExist(err) {
			log.Warn("Could not reset deferred file", "error", err)
		}
	}
	deferred, err := review.LoadDeferred(deferredPath)
	if err != nil {
		log.Warn("Could not load deferred file; treating as empty", "error", err)
	}
	deferred = review.ReconcileDeferred(deferred, threads, viewerLogin)

	// Run the pipeline: filter + classify.
	cfg := claude.DefaultConfig()
	items, pipelineErr := review.PipelineForThreads(
		ctx, threads, deferred, viewerLogin,
		claudeClassifierAdapter{cfg: *cfg},
		c.Concurrency,
	)
	if pipelineErr != nil {
		log.Warn("Classification errors occurred", "error", pipelineErr)
	}

	if len(items) == 0 {
		fmt.Fprintln(view, "no review threads to address")
		return nil
	}

	// Warn about base-branch discipline issues before the TUI launches.
	if err := warnBranchDiscipline(
		ctx, log, wt.RootDir(), svc, store, c.Branch, items,
	); err != nil {
		log.Warn("branch-discipline check failed; continuing", "err", err)
	}

	// Extract plugin.
	pluginDir, cleanup, err := plugin.ExtractPullAndAddress()
	if err != nil {
		return fmt.Errorf("extract plugin: %w", err)
	}
	defer cleanup()

	repoRoot := wt.RootDir()

	var summary review.WalkSummary

	if c.Batch {
		// Batch mode: build instructions, run one big session.
		instructionsPath := filepath.Join(pluginDir, "INSTRUCTIONS.md")
		runner := &batchRunnerAdapter{
			pluginDir: pluginDir,
			repoRoot:  repoRoot,
			log:       log,
			stdout:    view,
			stderr:    view,
		}
		subjectLookup := func(sha string) (string, error) {
			return claude.CommitSubject(ctx, log, repoRoot, sha)
		}
		summary, err = review.RunBatch(
			ctx, items, instructionsPath, runner, threader, subjectLookup,
		)
		if err != nil {
			return fmt.Errorf("batch run: %w", err)
		}
	} else {
		// Interactive mode: walk through items one by one.
		walker := &review.Walker{
			Items:  items,
			Poster: threader,
			FixRunner: &fixSessionAdapter{
				pluginDir: pluginDir,
				repoRoot:  repoRoot,
				log:       log,
				stdout:    view,
				stderr:    view,
			},
		}
		summary, err = walker.Run(ctx, view)
		if err != nil {
			return fmt.Errorf("interactive walk: %w", err)
		}

		// Persist newly deferred IDs.
		if len(summary.DeferredIDs) > 0 {
			deferred = append(deferred, summary.DeferredIDs...)
		}
	}

	// Save updated deferred list.
	if saveErr := review.SaveDeferred(deferredPath, deferred); saveErr != nil {
		log.Warn("Could not save deferred file", "error", saveErr)
	}

	// Print summary.
	printWalkSummary(view, log, summary)

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

// warnBranchDiscipline prints stderr warnings when items reference files
// last modified on a base branch in the stack, or when a restack would
// likely conflict on upper branches. Warnings are informational only —
// the caller should continue regardless of the returned error.
func warnBranchDiscipline(
	ctx context.Context,
	log *silog.Logger,
	repoRoot string,
	svc *spice.Service,
	store *state.Store,
	branch string,
	items []review.ClassifiedItem,
) error {
	stack, err := svc.ListStack(ctx, branch)
	if err != nil {
		log.Warn("Could not list stack for discipline check; skipping",
			"branch", branch, "error", err)
		return nil
	}

	// Build the ordered list of non-trunk stack branches.
	var stackBranches []string
	trunk := store.Trunk()
	for _, b := range stack {
		if b != trunk {
			stackBranches = append(stackBranches, b)
		}
	}
	if len(stackBranches) == 0 {
		return nil
	}

	// Determine the index of the current branch in the stack.
	currentIdx := -1
	for i, b := range stackBranches {
		if b == branch {
			currentIdx = i
			break
		}
	}

	// Per-item file-ownership warnings.
	for i, item := range items {
		if item.Classification == nil || item.Classification.FixPlan == "" {
			continue
		}
		file := item.Item.File
		if file == "" {
			continue
		}
		lastBranch, err := review.FileLastBranch(
			ctx, log, repoRoot, file, stackBranches,
		)
		if err != nil {
			// Non-fatal: log and move on.
			log.Warn("FileLastBranch check failed",
				"file", file, "error", err)
			continue
		}
		if lastBranch == "" || lastBranch == branch {
			continue
		}
		fmt.Fprintf(os.Stderr,
			"warning: item %d: file %s was last modified on base branch %q"+
				" in this stack;\n"+
				"  fixing here will apply only to the current PR's diff,"+
				" not the base.\n",
			i+1, file, lastBranch,
		)
	}

	// Pre-flight restack warning for upper branches.
	if currentIdx >= 0 && currentIdx < len(stackBranches)-1 {
		upperBranches := stackBranches[currentIdx+1:]
		conflicts, err := review.PreflightRestack(
			ctx, log, repoRoot, branch, upperBranches,
		)
		if err != nil {
			return fmt.Errorf("preflight restack: %w", err)
		}
		if len(conflicts) > 0 {
			fmt.Fprintln(os.Stderr,
				"warning: pre-flight: restack would likely conflict on:")
			for _, b := range conflicts {
				fmt.Fprintf(os.Stderr, "  * %s\n", b)
			}
		}
	}

	return nil
}
