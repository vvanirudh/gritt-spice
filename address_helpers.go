package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"go.abhg.dev/gs/internal/claude"
	"go.abhg.dev/gs/internal/forge"
	"go.abhg.dev/gs/internal/review"
	"go.abhg.dev/gs/internal/silog"
)

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
	// Aborted (claude exited non-zero) is reported as a partial-success
	// when commits exist: return the latest SHA alongside a wrapped
	// error so callers can use the commit but still surface the abort.
	// With no commits, propagate the original error verbatim.
	if runErr != nil {
		if res == nil || !res.Aborted || len(res.NewCommits) == 0 {
			return "", "", runErr
		}
		sha = res.NewCommits[len(res.NewCommits)-1]
		subject, _ = claude.CommitSubject(ctx, a.log, a.repoRoot, sha)
		return sha, subject, fmt.Errorf(
			"session aborted after %d commit(s): %w",
			len(res.NewCommits), runErr,
		)
	}
	if len(res.NewCommits) == 0 {
		return "", "", errors.New("agent made no commits")
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
	if err != nil && (res == nil || !res.Aborted) {
		return nil, nil, err
	}
	// On Aborted=true we still return any commits the session produced so
	// RunBatch can mark partial progress, but we propagate err so the
	// caller records the abort in summary.Errors.
	return res.PerItem, res.NewCommits, err
}

// noopReplyPoster satisfies review.ReplyPoster for commands
// (like branch checks) where items have no associated thread.
type noopReplyPoster struct{}

func (noopReplyPoster) PostReviewThreadReply(
	_ context.Context,
	_ forge.ReviewThreadID,
	_ string,
) (forge.ChangeCommentID, error) {
	return nil, nil
}

// printWalkSummary writes the final tally line and any per-item errors.
func printWalkSummary(
	w io.Writer,
	log *silog.Logger,
	summary review.WalkSummary,
) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "─── Summary ─────────────────────────────────")
	if summary.Addressed > 0 {
		fmt.Fprintf(w, "  ✓ %d addressed (commits + replies posted)\n",
			summary.Addressed)
	}
	if summary.Replied > 0 {
		fmt.Fprintf(w, "  ✓ %d reply-only posted\n", summary.Replied)
	}
	if summary.Skipped > 0 {
		fmt.Fprintf(w, "  → %d skipped\n", summary.Skipped)
	}
	if summary.Deferred > 0 {
		fmt.Fprintf(w, "  → %d deferred (will resurface next run)\n",
			summary.Deferred)
	}
	if len(summary.Errors) > 0 {
		fmt.Fprintf(w, "  ✗ %d error(s):\n", len(summary.Errors))
		for _, e := range summary.Errors {
			fmt.Fprintf(w, "      - %s\n", e)
			log.Warn("Error during walk", "detail", e)
		}
	}
	if summary.Addressed == 0 && summary.Replied == 0 &&
		summary.Skipped == 0 && summary.Deferred == 0 &&
		len(summary.Errors) == 0 {
		fmt.Fprintln(w, "  (no actions taken)")
	}
	fmt.Fprintln(w, "─────────────────────────────────────────────")
}
