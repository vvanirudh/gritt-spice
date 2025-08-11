package spice

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.abhg.dev/gs/internal/git"
	"go.abhg.dev/gs/internal/must"
	"go.abhg.dev/gs/internal/spice/state"
)

// RebaseRescueRequest is a request to rescue a rebase operation.
// Deprecated: Use RestackRescueRequest for new code.
type RebaseRescueRequest struct {
	// Err is the error that caused the rebase operation to be interrupted.
	Err error

	// Command is the command that should be run
	// after the rebase operation has been rescued.
	//
	// If this is unset, a continuation will NOT be recorded.
	Command []string

	// Branch is the branch on which the command should be run.
	//
	// If this is unset, the continuation will run on the interrupted
	// branch.
	Branch string

	// Message is the message that should be recorded
	// for debugging this continuation.
	Message string // optional
}

// RestackRescueRequest is a request to rescue a restack operation
// (either rebase or merge based).
type RestackRescueRequest struct {
	// Err is the error that caused the restack operation to be interrupted.
	Err error

	// Command is the command that should be run
	// after the restack operation has been rescued.
	//
	// If this is unset, a continuation will NOT be recorded.
	Command []string

	// Branch is the branch on which the command should be run.
	//
	// If this is unset, the continuation will run on the interrupted
	// branch.
	Branch string

	// Message is the message that should be recorded
	// for debugging this continuation.
	Message string // optional
}

// rescuedRebaseError helps differentiate between rescued rebases
// and non-rescued rebases so that we don't print the message twice
type rescuedRebaseError struct {
	err *git.RebaseInterruptError
}

func (r *rescuedRebaseError) Error() string {
	return r.err.Error()
}

// RebaseRescue helps operations continue from rebase conflicts.
// To use it, call RebaseRescue with the error that caused the rebase
// operation to be interrupted.
//
// For example:
//
//	func myOperation(...) error {
//		if err := repo.Rebase(ctx, ...); err != nil {
//			return svc.RebaseRescue(ctx, ...)
//		}
//		return nil
//	}
//
// The function returns an error back to the caller so that the program can
// exit.
//
// For commands that invoke other commands that may be interrupted by a rebase,
// assuming both commands are idempotent and re-entrant,
// the parent should also wrap the command in a rebase rescue operation.
// For example, if we have a leaf operation that rescues:
//
//	func childOperation(...) error {
//		if err := repo.Rebase(ctx, ...); err != nil {
//			return svc.RebaseRescue(ctx, ...)
//		}
//		return nil
//	}
//
//	func parentOperation(...) error {
//		for _, child := range children {
//			if err := childOperation(...); err != nil {
//				return svc.RebaseRescue(ctx, ...)
//			}
//		}
//	}
//
// Note that this tends to not be necessary for commands that end with a single
// child operation, e.g.
//
//	func parentOperation(...) error {
//		// ...
//		return childOperation(...)
//	}
//
// As at that point, re-running the child operation is sufficient.
func (s *Service) RebaseRescue(ctx context.Context, req RebaseRescueRequest) error {
	var (
		rescuedErr *rescuedRebaseError
		rebaseErr  *git.RebaseInterruptError
	)
	switch {
	case errors.As(req.Err, &rescuedErr):
		// Already rescued.
		// No need to print the error.

	case errors.As(req.Err, &rebaseErr):
		// First in a possible sequence of rescues up the call stack.
		// Print the error message, and clear the continuation stack.
		if _, err := s.store.TakeContinuations(ctx, "rebase rescue"); err != nil {
			return fmt.Errorf("clear rebase continuations: %w", err)
		}

		switch rebaseErr.Kind {
		case git.RebaseInterruptConflict:
			var msg strings.Builder
			fmt.Fprintf(&msg, "There was a conflict while rebasing.\n")
			fmt.Fprintf(&msg, "Resolve the conflict and run:\n")
			fmt.Fprintf(&msg, "  gs rebase continue\n")
			fmt.Fprintf(&msg, "Or abort the operation with:\n")
			fmt.Fprintf(&msg, "  gs rebase abort\n")
			s.log.Error(msg.String())
		case git.RebaseInterruptDeliberate:
			var msg strings.Builder
			fmt.Fprintf(&msg, "The rebase operation was interrupted with an 'edit' or 'break' command.\n")
			fmt.Fprintf(&msg, "When you're ready to continue, run:\n")
			fmt.Fprintf(&msg, "  gs rebase continue\n")
			fmt.Fprintf(&msg, "Or abort the operation with:\n")
			fmt.Fprintf(&msg, "  gs rebase abort\n")
			s.log.Info(msg.String())
		default:
			must.Failf("unexpected rebase interrupt kind: %v", rebaseErr.Kind)
		}

		rescuedErr = &rescuedRebaseError{err: rebaseErr}

	default:
		return req.Err
	}
	must.NotBeNilf(rescuedErr, "rescuedErr must be set at this point")

	// No continuation to record.
	if len(req.Command) == 0 {
		return rescuedErr
	}

	branch := req.Branch
	if branch == "" {
		branch = rebaseErr.State.Branch
	}

	msg := req.Message
	if msg == "" {
		msg = "interrupted: branch " + req.Branch
	}

	if err := s.store.AppendContinuations(ctx, msg, state.Continuation{
		Command: req.Command,
		Branch:  branch,
	}); err != nil {
		return fmt.Errorf("edit state: %w", err)
	}
	s.log.Debug("Pushed rebase continuation",
		"command", strings.Join(req.Command, " "),
		"branch", branch)

	return rescuedErr
}

// RestackRescue helps operations continue from restack conflicts (rebase or merge).
// This is a generalized version of RebaseRescue that works with both rebase and merge operations.
func (s *Service) RestackRescue(ctx context.Context, req RestackRescueRequest) error {
	var (
		rescuedErr    *rescuedRebaseError
		rebaseErr     *git.RebaseInterruptError
		mergeErr      *git.MergeInterruptError
		restackErr    *RestackInterruptError
	)

	switch {
	case errors.As(req.Err, &rescuedErr):
		// Already rescued.
		// No need to print the error.

	case errors.As(req.Err, &restackErr):
		// New generalized restack error.
		// Extract the underlying error and handle it appropriately.
		if _, err := s.store.TakeContinuations(ctx, "restack rescue"); err != nil {
			return fmt.Errorf("clear restack continuations: %w", err)
		}

		var msg strings.Builder
		switch restackErr.Method {
		case RestackMethodRebase:
			if errors.As(restackErr.Err, &rebaseErr) {
				switch rebaseErr.Kind {
				case git.RebaseInterruptConflict:
					fmt.Fprintf(&msg, "There was a conflict while rebasing.\n")
					fmt.Fprintf(&msg, "Resolve the conflict and run:\n")
					fmt.Fprintf(&msg, "  gs rebase continue\n")
					fmt.Fprintf(&msg, "Or abort the operation with:\n")
					fmt.Fprintf(&msg, "  gs rebase abort\n")
					s.log.Error(msg.String())
				case git.RebaseInterruptDeliberate:
					fmt.Fprintf(&msg, "The rebase operation was interrupted with an 'edit' or 'break' command.\n")
					fmt.Fprintf(&msg, "When you're ready to continue, run:\n")
					fmt.Fprintf(&msg, "  gs rebase continue\n")
					fmt.Fprintf(&msg, "Or abort the operation with:\n")
					fmt.Fprintf(&msg, "  gs rebase abort\n")
					s.log.Info(msg.String())
				default:
					must.Failf("unexpected rebase interrupt kind: %v", rebaseErr.Kind)
				}
			}

		case RestackMethodMerge:
			fmt.Fprintf(&msg, "There was a conflict while merging.\n")
			fmt.Fprintf(&msg, "Resolve the conflict and run:\n")
			fmt.Fprintf(&msg, "  gs continue\n")
			fmt.Fprintf(&msg, "Or abort the operation with:\n")
			fmt.Fprintf(&msg, "  gs abort\n")
			s.log.Error(msg.String())

		default:
			fmt.Fprintf(&msg, "There was a conflict during restacking.\n")
			fmt.Fprintf(&msg, "Resolve the conflict and run:\n")
			fmt.Fprintf(&msg, "  gs continue\n")
			fmt.Fprintf(&msg, "Or abort the operation with:\n")
			fmt.Fprintf(&msg, "  gs abort\n")
			s.log.Error(msg.String())
		}

		rescuedErr = &rescuedRebaseError{err: &git.RebaseInterruptError{
			Kind:  git.RebaseInterruptConflict,
			State: &git.RebaseState{Branch: restackErr.Branch},
			Err:   restackErr.Err,
		}}

	case errors.As(req.Err, &rebaseErr):
		// Legacy rebase error handling - delegate to RebaseRescue
		return s.RebaseRescue(ctx, RebaseRescueRequest(req))

	case errors.As(req.Err, &mergeErr):
		// Direct merge error - handle it
		if _, err := s.store.TakeContinuations(ctx, "merge rescue"); err != nil {
			return fmt.Errorf("clear merge continuations: %w", err)
		}

		var msg strings.Builder
		fmt.Fprintf(&msg, "There was a conflict while merging.\n")
		fmt.Fprintf(&msg, "Resolve the conflict and run:\n")
		fmt.Fprintf(&msg, "  gs continue\n")
		fmt.Fprintf(&msg, "Or abort the operation with:\n")
		fmt.Fprintf(&msg, "  gs abort\n")
		s.log.Error(msg.String())

		rescuedErr = &rescuedRebaseError{err: &git.RebaseInterruptError{
			Kind:  git.RebaseInterruptConflict,
			State: &git.RebaseState{Branch: mergeErr.Branch},
			Err:   mergeErr.Err,
		}}

	default:
		return req.Err
	}
	must.NotBeNilf(rescuedErr, "rescuedErr must be set at this point")

	// No continuation to record.
	if len(req.Command) == 0 {
		return rescuedErr
	}

	branch := req.Branch
	if branch == "" {
		// Try to extract branch from the specific error types
		if rebaseErr != nil {
			branch = rebaseErr.State.Branch
		} else if mergeErr != nil {
			branch = mergeErr.Branch
		} else if restackErr != nil {
			branch = restackErr.Branch
		}
	}

	msg := req.Message
	if msg == "" {
		msg = "interrupted: branch " + branch
	}

	if err := s.store.AppendContinuations(ctx, msg, state.Continuation{
		Command: req.Command,
		Branch:  branch,
	}); err != nil {
		return fmt.Errorf("edit state: %w", err)
	}
	s.log.Debug("Pushed restack continuation",
		"command", strings.Join(req.Command, " "),
		"branch", branch)

	return rescuedErr
}
