package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"go.abhg.dev/gs/internal/silog"
)

// MergeInterruptError indicates that a merge operation was interrupted
// due to conflicts.
type MergeInterruptError struct {
	// Branch is the branch being merged into.
	Branch string

	// Source is the commitish being merged.
	Source string

	// Err is the underlying error that caused the interruption.
	Err error
}

func (e *MergeInterruptError) Error() string {
	return fmt.Sprintf("merge of %s into %s interrupted: %v", e.Source, e.Branch, e.Err)
}

func (e *MergeInterruptError) Unwrap() error {
	return e.Err
}

// MergeRequest is a request to merge commits.
type MergeRequest struct {
	// Source is the commitish to merge.
	Source string

	// Message is the merge commit message.
	// If empty, a default message will be generated.
	Message string

	// NoCommit performs the merge but does not create a commit.
	NoCommit bool

	// NoFastForward forces a merge commit even when a fast-forward is possible.
	NoFastForward bool

	// Strategy specifies the merge strategy to use.
	// If empty, git's default strategy is used.
	Strategy string
}

// Merge performs a git merge operation.
// It returns [MergeInterruptError] for known merge interruptions.
func (w *Worktree) Merge(ctx context.Context, req MergeRequest) error {
	args := []string{
		// Never include advice on how to resolve merge conflicts.
		// We'll handle that ourselves.
		"-c", "advice.mergeConflict=false",
		"merge",
	}

	if req.NoCommit {
		args = append(args, "--no-commit")
	}
	if req.NoFastForward {
		args = append(args, "--no-ff")
	}
	if req.Strategy != "" {
		args = append(args, "--strategy", req.Strategy)
	}
	if req.Message != "" {
		args = append(args, "--message", req.Message)
	}

	args = append(args, req.Source)

	w.log.Debug("Merging",
		silog.NonZero("source", req.Source),
		silog.NonZero("strategy", req.Strategy),
	)

	cmd := w.gitCmd(ctx, args...).LogPrefix("git merge")
	if err := cmd.Run(w.exec); err != nil {
		return w.handleMergeError(ctx, req.Source, err)
	}

	return nil
}

func (w *Worktree) handleMergeError(ctx context.Context, source string, err error) error {
	originalErr := err
	if exitErr := new(exec.ExitError); !errors.As(err, &exitErr) {
		return fmt.Errorf("merge: %w", err)
	}

	// Check if we're in the middle of a merge conflict
	if state, mergeErr := w.MergeState(ctx); mergeErr == nil && state != nil {
		return &MergeInterruptError{
			Branch: state.Branch,
			Source: source,
			Err:    originalErr,
		}
	}

	return originalErr
}

// MergeState holds information about the current state of a merge operation.
type MergeState struct {
	// Branch is the branch being merged into.
	Branch string
	
	// Source is the commitish being merged (if available).
	Source string
}

// ErrNoMerge indicates that a merge is not in progress.
var ErrNoMerge = errors.New("no merge in progress")

// MergeState loads information about an ongoing merge,
// or [ErrNoMerge] if no merge is in progress.
func (w *Worktree) MergeState(ctx context.Context) (*MergeState, error) {
	// Check if MERGE_HEAD exists, which indicates a merge in progress
	cmd := w.gitCmd(ctx, "rev-parse", "--verify", "MERGE_HEAD")
	if err := cmd.Run(w.exec); err != nil {
		if exitErr := new(exec.ExitError); errors.As(err, &exitErr) {
			// MERGE_HEAD doesn't exist, no merge in progress
			return nil, ErrNoMerge
		}
		return nil, fmt.Errorf("check merge state: %w", err)
	}

	// Get current branch name
	cmd = w.gitCmd(ctx, "branch", "--show-current")
	out, err := cmd.Output(w.exec)
	if err != nil {
		return nil, fmt.Errorf("get current branch: %w", err)
	}

	branch := strings.TrimSpace(string(out))
	if branch == "" {
		branch = "HEAD" // detached HEAD
	}

	return &MergeState{
		Branch: branch,
		Source: "MERGE_HEAD", // We could parse this further if needed
	}, nil
}

// MergeContinueOptions holds options for continuing a merge operation.
type MergeContinueOptions struct {
	// Editor specifies the editor to use for the merge commit message.
	// If empty, the default editor will be used.
	Editor string
}

// MergeContinue continues an ongoing merge operation.
func (w *Worktree) MergeContinue(ctx context.Context, opts *MergeContinueOptions) error {
	if opts == nil {
		opts = &MergeContinueOptions{}
	}

	cmd := w.gitCmd(ctx, "commit", "--no-edit")
	if opts.Editor != "" {
		cmd.WithConfig(extraConfig{Editor: opts.Editor})
	}

	if err := cmd.Run(w.exec); err != nil {
		// For continue operations, we don't want to wrap errors as MergeInterruptError
		// since we're trying to resolve the interruption, not create a new one
		return fmt.Errorf("merge continue: %w", err)
	}

	return nil
}

// MergeAbort aborts an ongoing merge operation.
func (w *Worktree) MergeAbort(ctx context.Context) error {
	if err := w.gitCmd(ctx, "merge", "--abort").Run(w.exec); err != nil {
		return fmt.Errorf("merge abort: %w", err)
	}
	return nil
}