package forge

import "context"

// CopilotReviewerRequester is an optional capability implemented by
// a [Repository] that supports requesting GitHub Copilot Code Review
// on a change.
//
// Forges that don't support this capability simply don't implement
// the interface; callers should type-assert and fall back to a
// warning when the assertion fails.
type CopilotReviewerRequester interface {
	// RequestCopilotReview adds Copilot as a requested reviewer on
	// the given change.
	//
	// Implementations MUST be idempotent: if Copilot is already in
	// the change's requested reviewers OR has previously submitted a
	// review on this change, this should return (false, nil) without
	// making the request. This avoids piling up duplicate review
	// requests when the user resubmits the change after pushing new
	// commits. Returns (true, nil) when Copilot was newly requested.
	RequestCopilotReview(
		ctx context.Context,
		id ChangeID,
	) (requested bool, err error)
}
