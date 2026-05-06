package gitlab

import (
	"context"
	"fmt"

	gitlab "gitlab.com/gitlab-org/api/client-go"
	"go.abhg.dev/gs/internal/forge"
	"go.abhg.dev/gs/internal/git"
)

// ChangeStatuses retrieves compact statuses for the given changes in bulk.
func (r *Repository) ChangeStatuses(ctx context.Context, ids []forge.ChangeID) ([]forge.ChangeStatus, error) {
	mrIDs := make([]int64, len(ids))
	for i, id := range ids {
		mrIDs[i] = mustMR(id).Number
	}

	allStates := "all"
	mergeRequests, _, err := r.client.MergeRequests.ListProjectMergeRequests(
		r.repoID, &gitlab.ListProjectMergeRequestsOptions{
			IIDs:  &mrIDs,
			State: &allStates,
		},
		gitlab.WithContext(ctx),
	)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	// Create a map of MR IDs to MRs.
	mrMap := make(map[int64]*gitlab.BasicMergeRequest)
	for _, mr := range mergeRequests {
		mrMap[mr.IID] = mr
	}

	statuses := make([]forge.ChangeStatus, len(mrIDs))
	for i, id := range mrIDs {
		mr, ok := mrMap[id]
		if !ok {
			// Missing from response (deleted or inaccessible);
			// treat as open so downstream code skips it.
			statuses[i].State = forge.ChangeOpen
			continue
		}
		switch mr.State {
		case "opened":
			statuses[i].State = forge.ChangeOpen
		case "merged":
			statuses[i].State = forge.ChangeMerged
		case "closed":
			statuses[i].State = forge.ChangeClosed
		default:
			statuses[i].State = forge.ChangeOpen // default to open for unknown states
		}
		statuses[i].HeadHash = git.Hash(mr.SHA)
	}

	return statuses, nil
}

// ChangesDetails retrieves state, draft status, and review decision
// for the given changes in bulk.
func (r *Repository) ChangesDetails(ctx context.Context, ids []forge.ChangeID) ([]forge.ChangeDetails, error) {
	mrIDs := make([]int64, len(ids))
	for i, id := range ids {
		mrIDs[i] = mustMR(id).Number
	}

	allStates := "all"
	mergeRequests, _, err := r.client.MergeRequests.ListProjectMergeRequests(
		r.repoID, &gitlab.ListProjectMergeRequestsOptions{
			IIDs:  &mrIDs,
			State: &allStates,
		},
		gitlab.WithContext(ctx),
	)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	mrMap := make(map[int64]*gitlab.BasicMergeRequest)
	for _, mr := range mergeRequests {
		mrMap[mr.IID] = mr
	}

	details := make([]forge.ChangeDetails, len(mrIDs))
	for i, id := range mrIDs {
		mr, ok := mrMap[id]
		if !ok {
			// MR not found; return zero-value details.
			continue
		}
		details[i] = forge.ChangeDetails{
			State: forgeChangeState(mr.State),
			Draft: mr.Draft,
			ReviewDecision: gitlabReviewDecision(
				mr.Reviewers,
				mr.DetailedMergeStatus,
			),
		}
	}

	return details, nil
}

// gitlabReviewDecision maps GitLab reviewer and merge status info
// to a forge.ChangeReviewDecision.
//
// GitLab does not have a single "review decision" field like GitHub.
// We approximate it using:
//   - "approved" DetailedMergeStatus → ChangeReviewApproved
//   - non-empty reviewer list → ChangeReviewRequired
//   - otherwise → ChangeReviewNoReview
func gitlabReviewDecision(
	reviewers []*gitlab.BasicUser,
	detailedMergeStatus string,
) forge.ChangeReviewDecision {
	if detailedMergeStatus == "approved" {
		return forge.ChangeReviewApproved
	}

	if len(reviewers) > 0 {
		return forge.ChangeReviewRequired
	}

	return forge.ChangeReviewNoReview
}
