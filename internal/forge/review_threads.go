// Package forge provides an abstraction layer between git-spice
// and the underlying forge (e.g. GitHub, GitLab, Bitbucket).
package forge

import (
	"context"
	"errors"
	"io"
	"iter"
	"time"
)

// ErrCheckLogUnsupported is returned by ChangeChecksLister.GetCheckLog
// when the forge implementation cannot fetch raw check-run logs.
// Callers should detect this with errors.Is and degrade gracefully
// (for example, by linking to the check URL instead of showing logs).
var ErrCheckLogUnsupported = errors.New("check log fetching not supported by this forge")

// ReviewThreadID is a unique identifier for a review thread on a change.
type ReviewThreadID string

// ReviewReply is a single reply within a review thread.
type ReviewReply struct {
	// Author is the login of the user who wrote the reply.
	Author string

	// Body is the text content of the reply.
	Body string

	// CreatedAt is the time the reply was posted.
	CreatedAt time.Time
}

// ReviewThreadItem is a single review thread on a change,
// including its top-level comment and any replies.
type ReviewThreadItem struct {
	// ID is the unique identifier for the thread.
	ID ReviewThreadID

	// File is the path of the file the thread is anchored to.
	File string

	// LineRange is the inclusive [start, end] line range
	// within the file that the thread covers.
	// Both elements are 0 when the thread is not anchored to a line range
	// (e.g. file-level comments or comments on deleted files).
	LineRange [2]int

	// Hunk is the diff hunk context shown alongside the thread.
	Hunk string

	// Author is the login of the user who opened the thread.
	Author string

	// Body is the text of the top-level review comment.
	Body string

	// Replies contains the replies posted under the thread.
	Replies []ReviewReply

	// IsResolved reports whether the thread has been resolved.
	IsResolved bool

	// URL is the web address at which the thread can be viewed.
	URL string
}

// ListReviewThreadsOptions specifies filtering options
// for listing review threads on a change.
type ListReviewThreadsOptions struct {
	// IncludeResolved controls whether already-resolved threads
	// are included in the results.
	IncludeResolved bool

	// BotAllowlist is a list of bot logins whose threads
	// should be included in the results.
	//
	// Matching is performed case-insensitively against the bare login;
	// the trailing "[bot]" suffix that GitHub appends to bot usernames
	// is stripped before comparison.
	// An empty allowlist means no bot-authored threads are included.
	BotAllowlist []string
}

// ReviewThreadLister is an optional capability implemented by a [Repository]
// that supports listing and replying to pull request review threads.
//
// Callers should type-assert a Repository to this interface before use.
type ReviewThreadLister interface {
	// ListReviewThreads returns an iterator over review threads
	// on the given change.
	ListReviewThreads(
		ctx context.Context,
		id ChangeID,
		opts *ListReviewThreadsOptions,
	) iter.Seq2[*ReviewThreadItem, error]

	// PostReviewThreadReply posts a reply to an existing review thread
	// and returns the ID of the newly created comment.
	PostReviewThreadReply(
		ctx context.Context,
		threadID ReviewThreadID,
		body string,
	) (ChangeCommentID, error)
}

// CheckRunID is a unique identifier for a CI check run on a change.
type CheckRunID string

// ChangeCheckItem is a single CI check run associated with a change.
type ChangeCheckItem struct {
	// ID is the unique identifier for the check run.
	ID CheckRunID

	// Name is the human-readable name of the check.
	Name string

	// Status is the current execution status of the check
	// (e.g. "queued", "in_progress", "completed").
	Status string

	// Conclusion is the outcome of a completed check
	// (e.g. "success", "failure", "cancelled").
	// Empty if the check has not yet completed.
	Conclusion string

	// URL is the web address at which the check run can be viewed.
	URL string

	// StartedAt is the time the check run began executing.
	StartedAt time.Time

	// EndedAt is the time the check run finished executing.
	EndedAt time.Time
}

// ListChangeChecksOptions specifies filtering options
// for listing CI check runs on a change.
type ListChangeChecksOptions struct {
	// OnlyFailing limits results to failed check runs (conclusion of
	// "failure", "timed_out", "cancelled", or "action_required").
	// When the entire ListChangeChecksOptions is nil, implementations
	// default OnlyFailing to true.
	// When the struct is passed but the field is left at its zero value,
	// all check runs are returned.
	OnlyFailing bool
}

// ChangeChecksLister is an optional capability implemented by a [Repository]
// that supports listing CI check runs and fetching their logs.
//
// Callers should type-assert a Repository to this interface before use.
type ChangeChecksLister interface {
	// ListChangeChecks returns an iterator over CI check runs
	// associated with the given change.
	ListChangeChecks(
		ctx context.Context,
		id ChangeID,
		opts *ListChangeChecksOptions,
	) iter.Seq2[*ChangeCheckItem, error]

	// GetCheckLog fetches the log output for the given check run.
	// The caller is responsible for closing the returned reader.
	GetCheckLog(ctx context.Context, id CheckRunID) (io.ReadCloser, error)
}

// ViewerIdentifier is an optional capability implemented by a [Repository]
// that can identify the currently authenticated user.
//
// Callers should type-assert a Repository to this interface before use.
type ViewerIdentifier interface {
	// ViewerLogin returns the login name of the authenticated user.
	ViewerLogin(ctx context.Context) (string, error)
}
