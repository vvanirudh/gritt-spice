package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.abhg.dev/gs/internal/xec"
)

// MergeInterruptKind specifies the kind of merge interruption.
type MergeInterruptKind int

const (
	// MergeInterruptConflict indicates that a merge operation
	// was interrupted due to a conflict.
	MergeInterruptConflict MergeInterruptKind = iota

	// MergeInterruptDeliberate indicates that a merge operation
	// was interrupted deliberately by the user.
	MergeInterruptDeliberate
)

// MergeInterruptError indicates that a merging operation was interrupted.
// It includes the kind of interruption and the current merge state.
type MergeInterruptError struct {
	Kind  MergeInterruptKind
	State *MergeState // always non-nil

	// Err is non-nil only if the merge operation failed
	// due to a conflict.
	Err error
}

func (e *MergeInterruptError) Error() string {
	var msg strings.Builder
	msg.WriteString("merge")
	if e.State != nil && e.State.Branch != "" {
		fmt.Fprintf(&msg, " of %s", e.State.Branch)
	}
	msg.WriteString(" interrupted")
	switch e.Kind {
	case MergeInterruptConflict:
		msg.WriteString(" by a conflict")
	case MergeInterruptDeliberate:
		msg.WriteString(" deliberately")
	}
	if e.Err != nil {
		fmt.Fprintf(&msg, ": %v", e.Err)
	}
	return msg.String()
}

func (e *MergeInterruptError) Unwrap() error {
	return e.Err
}

// MergeRequest is a request to merge a commit or branch.
type MergeRequest struct {
	// Commit is the commit to merge into the current HEAD.
	// Either Commit or Branch must be specified.
	Commit string

	// Branch is the branch name to merge into the current HEAD.
	// Either Commit or Branch must be specified.
	Branch string

	// Message is the commit message for the merge commit.
	// If empty, the default merge message will be used.
	Message string

	// NoCommit indicates that the merge should be performed
	// but not committed automatically.
	NoCommit bool

	// NoFF forces creation of a merge commit even if the merge
	// could be resolved as a fast-forward.
	NoFF bool

	// Quiet reduces the output of the merge operation.
	Quiet bool
}

// Merge runs a git merge operation with the specified parameters.
// It returns [MergeInterruptError] for known merge interruptions.
func (w *Worktree) Merge(ctx context.Context, req MergeRequest) (err error) {
	if req.Commit == "" && req.Branch == "" {
		return errors.New("either Commit or Branch must be specified")
	}

	args := []string{
		// Never include advice on how to resolve merge conflicts.
		// We'll do that ourselves.
		"-c", "advice.mergeConflict=false",
		"merge",
	}

	if req.NoCommit {
		args = append(args, "--no-commit")
	}
	if req.NoFF {
		args = append(args, "--no-ff")
	}
	if req.Quiet {
		args = append(args, "--quiet")
	}
	if req.Message != "" {
		args = append(args, "-m", req.Message)
	}

	// Merge either the commit or branch
	if req.Commit != "" {
		args = append(args, req.Commit)
	} else {
		args = append(args, req.Branch)
	}

	w.log.Debug("Merging",
		"commit", req.Commit,
		"branch", req.Branch,
	)

	cmd := w.gitCmd(ctx, args...).WithLogPrefix("git merge")
	if err := cmd.Run(); err != nil {
		return w.handleMergeError(ctx, err)
	}
	return w.handleMergeFinish(ctx)
}

// MergeContinue continues an ongoing merge operation.
func (w *Worktree) MergeContinue(ctx context.Context) error {
	// Check if there are unmerged files
	var unmergedFiles []string
	for path := range w.ListFilesPaths(ctx, &ListFilesOptions{Unmerged: true}) {
		unmergedFiles = append(unmergedFiles, path)
	}
	if len(unmergedFiles) > 0 {
		return fmt.Errorf("unmerged files remain: %v", strings.Join(unmergedFiles, ", "))
	}

	// Commit the merge
	cmd := w.gitCmd(ctx, "commit", "--no-edit")
	if err := cmd.Run(); err != nil {
		return w.handleMergeError(ctx, err)
	}
	return w.handleMergeFinish(ctx)
}

func (w *Worktree) handleMergeError(ctx context.Context, err error) error {
	originalErr := err
	if exitErr := new(xec.ExitError); !errors.As(err, &exitErr) {
		return fmt.Errorf("merge: %w", err)
	}

	// If the merge operation actually ran, but failed,
	// we might be in the middle of a merge operation.
	state, err := w.MergeState(ctx)
	if err != nil {
		// Merge probably failed for a different reason,
		// so no need to log the state read failure verbosely.
		w.log.Debug("Failed to read merge state", "error", err)
		return originalErr
	}

	return &MergeInterruptError{
		Err:   originalErr,
		Kind:  MergeInterruptConflict,
		State: state,
	}
}

func (w *Worktree) handleMergeFinish(ctx context.Context) error {
	// If we have merge state after a successful return,
	// this was a deliberate break.
	if state, err := w.MergeState(ctx); err == nil {
		return &MergeInterruptError{
			Kind:  MergeInterruptDeliberate,
			State: state,
		}
	}

	return nil
}

// MergeAbort aborts an ongoing merge operation.
func (w *Worktree) MergeAbort(ctx context.Context) error {
	if err := w.gitCmd(ctx, "merge", "--abort").Run(); err != nil {
		return fmt.Errorf("merge abort: %w", err)
	}
	return nil
}

// MergeState holds information about the current state of a merge operation.
type MergeState struct {
	// Branch is the branch being merged, if available.
	Branch string
}

// ErrNoMerge indicates that a merge is not in progress.
var ErrNoMerge = errors.New("no merge in progress")

// MergeState loads information about an ongoing merge,
// or [ErrNoMerge] if no merge is in progress.
func (w *Worktree) MergeState(context.Context) (*MergeState, error) {
	// Merge state is stored inside .git/MERGE_HEAD
	// If this file exists, a merge is in progress.
	mergeHeadPath := filepath.Join(w.gitDir, "MERGE_HEAD")
	if _, err := os.Stat(mergeHeadPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoMerge
		}
		return nil, fmt.Errorf("check MERGE_HEAD: %w", err)
	}

	// Read the merge head to get the commit being merged
	mergeHead, err := os.ReadFile(mergeHeadPath)
	if err != nil {
		return nil, fmt.Errorf("read MERGE_HEAD: %w", err)
	}

	// Try to read MERGE_MSG to get the branch name
	var branch string
	mergeMsgPath := filepath.Join(w.gitDir, "MERGE_MSG")
	if mergeMsg, err := os.ReadFile(mergeMsgPath); err == nil {
		// The first line of MERGE_MSG typically contains the branch name
		// in the format: "Merge branch 'branchname'"
		lines := strings.Split(string(mergeMsg), "\n")
		if len(lines) > 0 {
			firstLine := lines[0]
			if strings.HasPrefix(firstLine, "Merge branch '") {
				if idx := strings.Index(firstLine[14:], "'"); idx > 0 {
					branch = firstLine[14 : 14+idx]
				}
			}
		}
	}

	// If we couldn't determine the branch from MERGE_MSG,
	// use the commit hash from MERGE_HEAD
	if branch == "" {
		branch = strings.TrimSpace(string(mergeHead))
	}

	return &MergeState{
		Branch: branch,
	}, nil
}
