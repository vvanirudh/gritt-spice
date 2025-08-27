package spice

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.abhg.dev/gs/internal/git"
	"go.abhg.dev/gs/internal/spice/state"
)

// ErrAlreadyRestacked indicates that a branch is already restacked
// on top of its base.
var ErrAlreadyRestacked = errors.New("branch is already restacked")

// RestackMethod specifies the method to use when restacking branches.
type RestackMethod int

const (
	// RestackMethodRebase uses git rebase to restack branches.
	// This is the default method and preserves individual commits.
	RestackMethodRebase RestackMethod = iota

	// RestackMethodMerge uses git merge to restack branches.
	// This method is better for branches with many commits.
	RestackMethodMerge
)

func (m RestackMethod) String() string {
	switch m {
	case RestackMethodRebase:
		return "rebase"
	case RestackMethodMerge:
		return "merge"
	default:
		return "unknown"
	}
}

// ParseRestackMethod parses a restack method string.
func ParseRestackMethod(s string) (RestackMethod, error) {
	switch s {
	case "rebase":
		return RestackMethodRebase, nil
	case "merge":
		return RestackMethodMerge, nil
	default:
		return RestackMethodRebase, fmt.Errorf("unknown restack method: %s", s)
	}
}

// RestackOptions specifies options for restacking operations.
type RestackOptions struct {
	// Method specifies the restacking method to use.
	// Defaults to RestackMethodRebase if unspecified.
	Method RestackMethod
}

// RestackResponse is the response to a restack operation.
type RestackResponse struct {
	Base string
}

// RestackInterruptError is a generalized error type for when a restack
// operation is interrupted, regardless of the method used.
type RestackInterruptError struct {
	// Method is the restacking method that was interrupted.
	Method RestackMethod

	// Branch is the branch being restacked.
	Branch string

	// Err is the underlying error that caused the interruption.
	Err error
}

func (e *RestackInterruptError) Error() string {
	return fmt.Sprintf("%s of %s interrupted: %v", e.Method, e.Branch, e.Err)
}

func (e *RestackInterruptError) Unwrap() error {
	return e.Err
}

// Restack restacks the given branch on top of its base branch,
// handling movement of the base branch if necessary.
//
// Returns [ErrAlreadyRestacked] if the branch does not need to be restacked.
func (s *Service) Restack(ctx context.Context, name string) (*RestackResponse, error) {
	return s.RestackWithOptions(ctx, name, RestackOptions{
		Method: RestackMethodRebase, // Default to existing behavior
	})
}

// RestackWithOptions restacks the given branch on top of its base branch
// using the specified options.
//
// Returns [ErrAlreadyRestacked] if the branch does not need to be restacked.
func (s *Service) RestackWithOptions(ctx context.Context, name string, opts RestackOptions) (*RestackResponse, error) {
	b, err := s.LookupBranch(ctx, name)
	if err != nil {
		return nil, err // includes ErrNotExist
	}

	err = s.VerifyRestacked(ctx, name)
	if err == nil {
		// Case:
		// The branch is already on top of its base branch
		return nil, ErrAlreadyRestacked
	}
	var restackErr *BranchNeedsRestackError
	if !errors.As(err, &restackErr) {
		return nil, fmt.Errorf("verify restacked: %w", err)
	}

	// The branch needs to be restacked on top of its base branch.
	// We will proceed with the restack.

	baseHash := restackErr.BaseHash
	upstream := b.BaseHash

	// Case:
	// Recorded base hash is super out of date,
	// and is not an ancestor of the current branch.
	// In that case, use fork point as a hail mary
	// to guess the upstream start point.
	//
	// For context, fork point attempts to find the point
	// where the current branch diverged from the branch it
	// was originally forked from.
	// For example, given:
	//
	//  ---X---A'---o foo
	//      \
	//       A
	//        \
	//         B---o---o bar
	//
	// If bar branched from foo, when foo was at A,
	// and then we amended foo to get A',
	// bar will still refer to A.
	//
	// In this case, merge-base --fork-point will give us A,
	// and that should be the upstream (commit to start rebasing from)
	// if the recorded base hash is out of date
	// because the user changed something externally.
	if !s.repo.IsAncestor(ctx, baseHash, b.Head) {
		forkPoint, err := s.repo.ForkPoint(ctx, b.Base, name)
		if err == nil {
			if upstream != forkPoint {
				s.log.Debug("Recorded base hash is out of date. Restacking from fork point.",
					"base", b.Base,
					"branch", name,
					"forkPoint", forkPoint)
			}
			upstream = forkPoint
		}
	}

	// Perform the restack using the specified method
	switch opts.Method {
	case RestackMethodRebase:
		if err := s.wt.Rebase(ctx, git.RebaseRequest{
			Onto:      baseHash.String(),
			Upstream:  upstream.String(),
			Branch:    name,
			Autostash: true,
			Quiet:     true,
		}); err != nil {
			var rebaseErr *git.RebaseInterruptError
			if errors.As(err, &rebaseErr) {
				return nil, &RestackInterruptError{
					Method: RestackMethodRebase,
					Branch: name,
					Err:    err,
				}
			}
			return nil, fmt.Errorf("rebase: %w", err)
		}

	case RestackMethodMerge:
		if err := s.restackWithMerge(ctx, name, baseHash, b.Base); err != nil {
			var mergeErr *git.MergeInterruptError
			if errors.As(err, &mergeErr) {
				return nil, &RestackInterruptError{
					Method: RestackMethodMerge,
					Branch: name,
					Err:    err,
				}
			}
			return nil, fmt.Errorf("merge: %w", err)
		}

	default:
		return nil, fmt.Errorf("unsupported restack method: %v", opts.Method)
	}

	tx := s.store.BeginBranchTx()
	if err := tx.Upsert(ctx, state.UpsertRequest{
		Name:     name,
		BaseHash: baseHash,
	}); err != nil {
		return nil, fmt.Errorf("update base hash of %v: %w", name, err)
	}

	if err := tx.Commit(ctx, fmt.Sprintf("%v: restacked on %v", name, b.Base)); err != nil {
		return nil, fmt.Errorf("update state: %w", err)
	}

	return &RestackResponse{
		Base: b.Base,
	}, nil
}

// BranchNeedsRestackError is returned by [Service.VerifyRestacked]
// when a branch needs to be restacked.
type BranchNeedsRestackError struct {
	// Base is the name of the base branch for the branch.
	Base string

	// BaseHash is the hash of the base branch.
	// Note that this is the actual hash, not the hash stored in state.
	BaseHash git.Hash
}

func (e *BranchNeedsRestackError) Error() string {
	return fmt.Sprintf("branch needs to be restacked on top of %v", e.Base)
}

// VerifyRestacked is a version of CheckRestacked
// that ignores the returned base branch hash.
func (s *Service) VerifyRestacked(ctx context.Context, name string) error {
	_, err := s.CheckRestacked(ctx, name)
	return err
}

// CheckRestacked verifies that the given branch is on top of its base branch.
// It updates the base branch hash if the hash is out of date,
// but the branch is restacked properly.
//
// It returns the actual hash of the base branch in case of succses,
// [ErrNeedsRestack] if the branch needs to be restacked,
// [state.ErrNotExist] if the branch is not tracked.
// Any other error indicates a problem with checking the branch.
func (s *Service) CheckRestacked(ctx context.Context, name string) (baseHash git.Hash, err error) {
	// A branch needs to be restacked if
	// its merge base with its base branch
	// is not its base branch's head.
	//
	// That is, the branch is not on top of its base branch's current head.
	b, err := s.LookupBranch(ctx, name)
	if err != nil {
		return git.ZeroHash, err
	}

	baseHash, err = s.repo.PeelToCommit(ctx, b.Base)
	if err != nil {
		if errors.Is(err, git.ErrNotExist) {
			return git.ZeroHash, fmt.Errorf("base branch %v does not exist", b.Base)
		}
		return git.ZeroHash, fmt.Errorf("find commit for %v: %w", b.Base, err)
	}

	if !s.repo.IsAncestor(ctx, baseHash, b.Head) {
		return git.ZeroHash, &BranchNeedsRestackError{
			Base:     b.Base,
			BaseHash: baseHash,
		}
	}

	// Branch does not need to be restacked
	// but the base hash stored in state may be out of date.
	if b.BaseHash != baseHash {
		s.log.Debug("Updating recorded base hash", "branch", name, "base", b.Base)

		tx := s.store.BeginBranchTx()
		if err := tx.Upsert(ctx, state.UpsertRequest{
			Name:     name,
			BaseHash: baseHash,
		}); err != nil {
			s.log.Warn("Failed to update recorded base hash", "error", err)
			return git.ZeroHash, nil
		}

		if err := tx.Commit(ctx, fmt.Sprintf("%v: branch was restacked externally", name)); err != nil {
			// This isn't a critical error. Just log it.
			s.log.Warn("Failed to update state", "error", err)
		}
	}

	return baseHash, nil
}

// restackWithMerge performs a merge-based restack of the given branch.
// This is a simplified implementation that uses git operations directly.
func (s *Service) restackWithMerge(ctx context.Context, branchName string, newBase git.Hash, baseName string) error {
	// Save current branch to restore later
	currentBranch, err := s.wt.CurrentBranch(ctx)
	if err != nil {
		return fmt.Errorf("get current branch: %w", err)
	}

	// Check if we're being called after a merge was completed
	// This can happen when continuation runs after conflict resolution
	headCommit, err := s.repo.PeelToCommit(ctx, "HEAD")
	if err == nil {
		// Check if HEAD commit message indicates it's a restack merge commit
		subject, err := s.repo.CommitSubject(ctx, headCommit.String())
		if err == nil && strings.Contains(subject, fmt.Sprintf("Restack %s onto %s via merge", branchName, baseName)) {
			s.log.Debugf("restackWithMerge: merge already completed, HEAD=%s points to restack merge commit: %s", headCommit, subject)
			// The merge is already done, we just need to update the branch pointer
			s.log.Debugf("restackWithMerge: updating branch %s to point to existing merge commit %s", branchName, headCommit)
			
			// If we're currently on the branch being updated, checkout detached HEAD first
			if currentBranch == branchName {
				if err := s.wt.Checkout(ctx, headCommit.String()); err != nil {
					return fmt.Errorf("checkout detached HEAD: %w", err)
				}
			}
			
			if err := s.repo.CreateBranch(ctx, git.CreateBranchRequest{
				Name:  branchName,
				Head:  headCommit.String(),
				Force: true, // Overwrite existing branch
			}); err != nil {
				return fmt.Errorf("update branch pointer to existing merge commit: %w", err)
			}

			// Restore original branch if needed
			if currentBranch == branchName {
				if err := s.wt.Checkout(ctx, branchName); err != nil {
					return fmt.Errorf("checkout restacked branch: %w", err)
				}
			} else if currentBranch != "" {
				if err := s.wt.Checkout(ctx, currentBranch); err != nil {
					s.log.Warn("Failed to restore original branch", "branch", currentBranch, "error", err)
				}
			}
			s.log.Debugf("restackWithMerge: successfully completed restack with existing merge commit")
			return nil
		}
	}

	// CRITICAL FIX: Get the current tip of the branch being restacked
	branchCommit, err := s.repo.PeelToCommit(ctx, branchName)
	if err != nil {
		return fmt.Errorf("get branch commit %s: %w", branchName, err)
	}
	
	// Checkout the branch being restacked (detached HEAD) to merge base into it
	// This ensures we merge the base INTO the feature, not feature into base
	if err := s.wt.Checkout(ctx, branchCommit.String()); err != nil {
		return fmt.Errorf("checkout branch being restacked %s: %w", branchName, err)
	}

	// Merge the new base INTO the feature branch (correct direction)
	mergeMsg := fmt.Sprintf("Restack %s onto %s via merge", branchName, baseName)
	if err := s.wt.Merge(ctx, git.MergeRequest{
		Source:        baseName, // Merge the BASE into current HEAD (the feature branch)
		Message:       mergeMsg,
		NoFastForward: true, // Always create a merge commit
	}); err != nil {
		return fmt.Errorf("merge %s into %s: %w", baseName, branchName, err)
	}

	// Get the merge commit hash
	mergeCommit, err := s.repo.PeelToCommit(ctx, "HEAD")
	if err != nil {
		return fmt.Errorf("get merge commit: %w", err)
	}

	s.log.Debugf("restackWithMerge: updating branch %s to point to merge commit %s", branchName, mergeCommit)
	
	// If we need to update a branch that was originally checked out, we need to stay in detached HEAD
	// until we update the branch pointer, then check it out again
	needToRestoreBranch := currentBranch == branchName
	
	// Update the feature branch pointer to the merge commit by force-creating it
	if err := s.repo.CreateBranch(ctx, git.CreateBranchRequest{
		Name:  branchName,
		Head:  mergeCommit.String(),
		Force: true, // Overwrite existing branch
	}); err != nil {
		return fmt.Errorf("update branch pointer: %w", err)
	}
	s.log.Debugf("restackWithMerge: successfully updated branch %s", branchName)

	// Restore original branch if needed
	if needToRestoreBranch {
		if err := s.wt.Checkout(ctx, branchName); err != nil {
			return fmt.Errorf("checkout restacked branch: %w", err)
		}
	} else if currentBranch != "" {
		if err := s.wt.Checkout(ctx, currentBranch); err != nil {
			s.log.Warn("Failed to restore original branch", "branch", currentBranch, "error", err)
		}
	}

	return nil
}
