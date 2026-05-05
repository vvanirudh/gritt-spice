package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

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

// branchChecksCmd fetches failing CI checks for a branch's PR
// and walks through them interactively (or in batch) with Claude AI.
type branchChecksCmd struct {
	Branch string `help:"Branch to fetch checks for (defaults to current)" predictor:"trackedBranches"`
	Batch  bool   `help:"Run all items in one Claude session"`

	IncludePassing bool `help:"Include passing/in_progress checks too (default: only failing)"`
	Concurrency    int  `help:"Parallel classifications" default:"4"`
}

func (*branchChecksCmd) Help() string {
	return `Fetches failing CI checks for the current branch's pull request,
classifies them with Claude AI, and walks through them one by one.

For each check, you can choose to:
  - address: spawn a Claude session to make commits fixing the failure
  - skip: move to the next check without action
  - defer: record the check for later

Use --batch to address all checks in a single Claude session instead.
Use --include-passing to also process passing or in-progress checks.
`
}

func (c *branchChecksCmd) Run(
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

	// Capability check: forge must support check listing.
	checker, ok := forgeRepo.(forge.ChangeChecksLister)
	if !ok {
		return fmt.Errorf(
			"forge %q does not support check listing",
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

	// Fetch CI checks.
	var checks []*forge.ChangeCheckItem
	for check, err := range checker.ListChangeChecks(
		ctx, prID,
		&forge.ListChangeChecksOptions{OnlyFailing: !c.IncludePassing},
	) {
		if err != nil {
			return fmt.Errorf("list checks: %w", err)
		}
		checks = append(checks, check)
	}

	if len(checks) == 0 {
		fmt.Fprintln(view, "no failing checks")
		return nil
	}

	// Classify checks, fetching logs for bodies.
	cfg := claude.DefaultConfig()
	classifier := claudeClassifierAdapter{cfg: *cfg}

	items, pipelineErr := pipelineForChecks(
		ctx, checker, checks, classifier, c.Concurrency,
	)
	if pipelineErr != nil {
		log.Warn("Classification errors occurred", "error", pipelineErr)
	}

	if len(items) == 0 {
		fmt.Fprintln(view, "no failing checks")
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
			ctx, items, instructionsPath, runner,
			noopReplyPoster{}, subjectLookup,
		)
		if err != nil {
			return fmt.Errorf("batch run: %w", err)
		}
	} else {
		// Interactive mode: walk through items one by one.
		walker := &review.Walker{
			Items:  items,
			Poster: noopReplyPoster{},
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
	}

	// Print summary.
	printWalkSummary(view, log, summary)

	return nil
}

// pipelineForChecks fetches logs for each check, builds claude.Items,
// and classifies them with bounded concurrency, preserving order.
//
// If the classifier returns an error for an item, that item is still
// included with Category: "unclassified" and the first such error
// is returned alongside the results.
//
// concurrency < 1 is treated as 1.
func pipelineForChecks(
	ctx context.Context,
	checker forge.ChangeChecksLister,
	checks []*forge.ChangeCheckItem,
	classifier review.Classifier,
	concurrency int,
) ([]review.ClassifiedItem, error) {
	if concurrency < 1 {
		concurrency = 1
	}

	results := make([]review.ClassifiedItem, len(checks))

	type workResult struct {
		index int
		item  review.ClassifiedItem
		err   error
	}

	sem := make(chan struct{}, concurrency)
	out := make(chan workResult, len(checks))

	for i, check := range checks {
		sem <- struct{}{}
		go func(i int, check *forge.ChangeCheckItem) {
			defer func() { <-sem }()

			body := checkBody(ctx, checker, check)

			item := &claude.Item{
				Kind:  "check",
				Title: check.Name,
				Body:  body,
				File:  "",
				// Source is *forge.ChangeCheckItem so walker
				// type-asserts to *forge.ReviewThreadItem → nil,
				// treating this as a check (no reply posting).
				Source: check,
			}
			c, err := classifier.Classify(ctx, item)
			if err != nil {
				c = &claude.Classification{Category: "unclassified"}
			}
			out <- workResult{
				index: i,
				item:  review.ClassifiedItem{Item: item, Classification: c},
				err:   err,
			}
		}(i, check)
	}

	// Drain results.
	var firstErr error
	for range checks {
		r := <-out
		results[r.index] = r.item
		if r.err != nil && firstErr == nil {
			firstErr = r.err
		}
	}

	return results, firstErr
}

// checkBody returns the log body for a check.
// If the forge does not support log fetching, falls back to a summary line.
func checkBody(
	ctx context.Context,
	checker forge.ChangeChecksLister,
	check *forge.ChangeCheckItem,
) string {
	rc, err := checker.GetCheckLog(ctx, check.ID)
	if err != nil {
		if errors.Is(err, forge.ErrCheckLogUnsupported) {
			// Log fetching not supported; synthesise a minimal body.
			return fmt.Sprintf(
				"Check %q ended with conclusion %q (status: %s).\n"+
					"View details at: %s",
				check.Name, check.Conclusion, check.Status, check.URL,
			)
		}
		// Unexpected error; still degrade gracefully.
		return fmt.Sprintf(
			"Check %q ended with conclusion %q (status: %s).\n"+
				"Log unavailable: %v\nView details at: %s",
			check.Name, check.Conclusion, check.Status, err, check.URL,
		)
	}
	defer func() { _ = rc.Close() }()

	data, readErr := io.ReadAll(rc)
	if readErr != nil {
		return fmt.Sprintf(
			"Check %q ended with conclusion %q.\n"+
				"Log read error: %v\nView details at: %s",
			check.Name, check.Conclusion, readErr, check.URL,
		)
	}

	// Trim to a reasonable tail for the classifier prompt.
	const maxBytes = 8192
	if len(data) > maxBytes {
		data = data[len(data)-maxBytes:]
	}
	return string(data)
}
