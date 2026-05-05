package github

import (
	"context"
	"fmt"
	"io"
	"iter"
	"strings"

	"github.com/shurcooL/githubv4"
	"go.abhg.dev/gs/internal/forge"
)

var _ forge.ChangeChecksLister = (*Repository)(nil)

// ListChangeChecks returns an iterator over CI check runs
// associated with the given change.
// If opts is nil, it defaults to &forge.ListChangeChecksOptions{OnlyFailing: true}.
func (r *Repository) ListChangeChecks(
	ctx context.Context,
	id forge.ChangeID,
	opts *forge.ListChangeChecksOptions,
) iter.Seq2[*forge.ChangeCheckItem, error] {
	if opts == nil {
		opts = &forge.ListChangeChecksOptions{OnlyFailing: true}
	}

	gqlID, err := r.graphQLID(ctx, mustPR(id))
	if err != nil {
		return func(yield func(*forge.ChangeCheckItem, error) bool) {
			yield(nil, err)
		}
	}

	return func(yield func(*forge.ChangeCheckItem, error) bool) {
		var q struct {
			Node struct {
				PullRequest struct {
					Commits struct {
						Nodes []struct {
							Commit struct {
								CheckSuites struct {
									Nodes []struct {
										CheckRuns struct {
											Nodes []struct {
												ID          githubv4.ID        `graphql:"id"`
												Name        string             `graphql:"name"`
												Status      string             `graphql:"status"`
												Conclusion  *string            `graphql:"conclusion"`
												URL         string             `graphql:"url"`
												StartedAt   *githubv4.DateTime `graphql:"startedAt"`
												CompletedAt *githubv4.DateTime `graphql:"completedAt"`
											} `graphql:"nodes"`
										} `graphql:"checkRuns(first: 100)"`
									} `graphql:"nodes"`
								} `graphql:"checkSuites(first: 50)"`
							} `graphql:"commit"`
						} `graphql:"nodes"`
					} `graphql:"commits(last: 1)"`
				} `graphql:"... on PullRequest"`
			} `graphql:"node(id: $id)"`
		}

		if err := r.client.Query(ctx, &q, map[string]any{
			"id": gqlID,
		}); err != nil {
			yield(nil, fmt.Errorf("list change checks: %w", err))
			return
		}

		// Guard against zero commits (e.g., a freshly created PR).
		if len(q.Node.PullRequest.Commits.Nodes) == 0 {
			return
		}

		commit := q.Node.PullRequest.Commits.Nodes[0].Commit
		for _, suite := range commit.CheckSuites.Nodes {
			for _, run := range suite.CheckRuns.Nodes {
				conclusion := ""
				if run.Conclusion != nil {
					conclusion = strings.ToLower(*run.Conclusion)
				}

				// Apply OnlyFailing filter.
				if opts.OnlyFailing {
					switch conclusion {
					case "failure", "timed_out", "cancelled", "action_required":
						// Include this run.
					default:
						continue
					}
				}

				item := &forge.ChangeCheckItem{
					ID:         forge.CheckRunID(fmt.Sprintf("%v", run.ID)),
					Name:       run.Name,
					Status:     strings.ToLower(run.Status),
					Conclusion: conclusion,
					URL:        run.URL,
				}
				if run.StartedAt != nil {
					item.StartedAt = run.StartedAt.Time
				}
				if run.CompletedAt != nil {
					item.EndedAt = run.CompletedAt.Time
				}

				if !yield(item, nil) {
					return
				}
			}
		}
	}
}

// GetCheckLog fetches the log output for the given check run.
//
// TODO(v2): Implement by plumbing a *http.Client through NewRepository
// to call GET /repos/{owner}/{repo}/check-runs/{check_run_id}/logs.
// Currently only a *githubv4.Client is available on Repository.
func (r *Repository) GetCheckLog(
	_ context.Context,
	runID forge.CheckRunID,
) (io.ReadCloser, error) {
	return nil, fmt.Errorf("%w (run id %q)", forge.ErrCheckLogUnsupported, runID)
}
