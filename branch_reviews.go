package main

import (
	"context"
	"fmt"
	"io"
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
	fmt.Fprintf(view,
		"addressed=%d replied=%d skipped=%d deferred=%d errors=%d\n",
		summary.Addressed,
		summary.Replied,
		summary.Skipped,
		summary.Deferred,
		len(summary.Errors),
	)
	for _, e := range summary.Errors {
		log.Warn("Error during review walk", "detail", e)
	}

	return nil
}

// parseCSV splits a comma-separated string into trimmed, non-empty tokens.
func parseCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// claudeClassifierAdapter implements review.Classifier using claude.ClassifyItem.
type claudeClassifierAdapter struct {
	cfg claude.Config
}

func (a claudeClassifierAdapter) Classify(
	ctx context.Context,
	item *claude.Item,
) (*claude.Classification, error) {
	return claude.ClassifyItem(ctx, a.cfg, item)
}

// fixSessionAdapter implements review.FixRunner using claude.FixSession
// for a single item.
type fixSessionAdapter struct {
	pluginDir string
	repoRoot  string
	log       *silog.Logger
	stdout    io.Writer
	stderr    io.Writer
}

func (a *fixSessionAdapter) Run(
	ctx context.Context,
	instructions string,
) (sha, subject string, err error) {
	instructionsPath := filepath.Join(a.pluginDir, "INSTRUCTIONS.md")
	if err := os.WriteFile(
		instructionsPath, []byte(instructions), 0o644,
	); err != nil {
		return "", "", fmt.Errorf("write instructions: %w", err)
	}

	s := &claude.FixSession{
		PluginDir:    a.pluginDir,
		Instructions: instructionsPath,
		RepoRoot:     a.repoRoot,
		Stdout:       a.stdout,
		Stderr:       a.stderr,
		Log:          a.log,
	}
	res, runErr := s.Run(ctx)
	if runErr != nil && !res.Aborted {
		return "", "", runErr
	}
	if len(res.NewCommits) == 0 {
		return "", "", fmt.Errorf("agent made no commits")
	}
	sha = res.NewCommits[len(res.NewCommits)-1]
	subject, _ = claude.CommitSubject(ctx, a.log, a.repoRoot, sha)
	return sha, subject, nil
}

// batchRunnerAdapter implements review.BatchRunner using claude.FixSession.
type batchRunnerAdapter struct {
	pluginDir string
	repoRoot  string
	log       *silog.Logger
	stdout    io.Writer
	stderr    io.Writer
}

func (a *batchRunnerAdapter) Run(
	ctx context.Context,
	instructionsPath string,
) (map[string][]string, []string, error) {
	s := &claude.FixSession{
		PluginDir:    a.pluginDir,
		Instructions: instructionsPath,
		RepoRoot:     a.repoRoot,
		Stdout:       a.stdout,
		Stderr:       a.stderr,
		Log:          a.log,
	}
	res, err := s.Run(ctx)
	if err != nil && !res.Aborted {
		return nil, nil, err
	}
	return res.PerItem, res.NewCommits, nil
}
