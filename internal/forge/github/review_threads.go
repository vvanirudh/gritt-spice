package github

import (
	"context"
	"fmt"
	"iter"
	"strings"

	"github.com/shurcooL/githubv4"
	"go.abhg.dev/gs/internal/forge"
)

var _ forge.ReviewThreadLister = (*Repository)(nil)

// ListReviewThreads returns an iterator over pull request review threads.
// Threads are filtered according to opts:
// resolved threads are excluded unless IncludeResolved is set,
// and bot-authored threads are excluded unless the bot login
// (with "[bot]" suffix stripped) is in BotAllowlist.
func (r *Repository) ListReviewThreads(
	ctx context.Context,
	id forge.ChangeID,
	opts *forge.ListReviewThreadsOptions,
) iter.Seq2[*forge.ReviewThreadItem, error] {
	if opts == nil {
		opts = &forge.ListReviewThreadsOptions{}
	}

	gqlID, err := r.graphQLID(ctx, mustPR(id))
	if err != nil {
		return func(yield func(*forge.ReviewThreadItem, error) bool) {
			yield(nil, err)
		}
	}

	return func(yield func(*forge.ReviewThreadItem, error) bool) {
		type commentNode struct {
			Author struct {
				Login string `graphql:"login"`
			} `graphql:"author"`
			Body      string            `graphql:"body"`
			DiffHunk  string            `graphql:"diffHunk"`
			URL       string            `graphql:"url"`
			CreatedAt githubv4.DateTime `graphql:"createdAt"`
			StartLine *int              `graphql:"startLine"`
			Line      *int              `graphql:"line"`
		}

		var q struct {
			Node struct {
				PullRequest struct {
					ReviewThreads struct {
						PageInfo struct {
							EndCursor   githubv4.String `graphql:"endCursor"`
							HasNextPage bool            `graphql:"hasNextPage"`
						} `graphql:"pageInfo"`
						Nodes []struct {
							ID         githubv4.ID `graphql:"id"`
							Path       string      `graphql:"path"`
							IsResolved bool        `graphql:"isResolved"`
							Comments   struct {
								Nodes []commentNode `graphql:"nodes"`
							} `graphql:"comments(first: 100)"`
						} `graphql:"nodes"`
					} `graphql:"reviewThreads(first: $first, after: $after)"`
				} `graphql:"... on PullRequest"`
			} `graphql:"node(id: $id)"`
		}

		variables := map[string]any{
			"id":    gqlID,
			"first": githubv4.Int(50),
			"after": (*githubv4.String)(nil),
		}

		for pageNum := 1; true; pageNum++ {
			if err := r.client.Query(ctx, &q, variables); err != nil {
				yield(nil, fmt.Errorf(
					"list review threads (page %d): %w",
					pageNum, err,
				))
				return
			}

			for _, thread := range q.Node.PullRequest.ReviewThreads.Nodes {
				// Skip resolved threads unless requested.
				if thread.IsResolved && !opts.IncludeResolved {
					continue
				}

				// We need at least one comment to determine author.
				if len(thread.Comments.Nodes) == 0 {
					continue
				}

				first := thread.Comments.Nodes[0]
				author := first.Author.Login

				// Apply bot filtering.
				if isBot(author) {
					bare := stripBotSuffix(author)
					if !inBotAllowlist(bare, opts.BotAllowlist) {
						continue
					}
				}

				// Build line range.
				var lineRange [2]int
				if first.Line != nil {
					end := *first.Line
					start := end
					if first.StartLine != nil {
						start = *first.StartLine
					}
					lineRange = [2]int{start, end}
				}

				// Build replies (all comments after the first).
				var replies []forge.ReviewReply
				for _, c := range thread.Comments.Nodes[1:] {
					replies = append(replies, forge.ReviewReply{
						Author:    c.Author.Login,
						Body:      c.Body,
						CreatedAt: c.CreatedAt.Time,
					})
				}

				item := &forge.ReviewThreadItem{
					ID:         forge.ReviewThreadID(fmt.Sprintf("%v", thread.ID)),
					File:       thread.Path,
					LineRange:  lineRange,
					Hunk:       first.DiffHunk,
					Author:     author,
					Body:       first.Body,
					Replies:    replies,
					IsResolved: thread.IsResolved,
					URL:        first.URL,
				}

				if !yield(item, nil) {
					return
				}
			}

			if !q.Node.PullRequest.ReviewThreads.PageInfo.HasNextPage {
				return
			}

			variables["after"] = q.Node.PullRequest.ReviewThreads.PageInfo.EndCursor
		}
	}
}

// PostReviewThreadReply posts a reply to an existing pull request review thread.
func (r *Repository) PostReviewThreadReply(
	ctx context.Context,
	threadID forge.ReviewThreadID,
	body string,
) (forge.ChangeCommentID, error) {
	var m struct {
		AddPullRequestReviewThreadReply struct {
			Comment struct {
				ID  githubv4.ID `graphql:"id"`
				URL string      `graphql:"url"`
			} `graphql:"comment"`
		} `graphql:"addPullRequestReviewThreadReply(input: $input)"`
	}

	input := githubv4.AddPullRequestReviewThreadReplyInput{
		PullRequestReviewThreadID: githubv4.ID(threadID),
		Body:                      githubv4.String(body),
	}

	if err := r.client.Mutate(ctx, &m, input, nil); err != nil {
		return nil, fmt.Errorf("post review thread reply: %w", err)
	}

	c := m.AddPullRequestReviewThreadReply.Comment
	r.log.Debug("Posted review thread reply", "url", c.URL)
	return &PRComment{
		GQLID: c.ID,
		URL:   c.URL,
	}, nil
}

// isBot reports whether login has a "[bot]" suffix,
// indicating a GitHub App or bot account.
func isBot(login string) bool {
	return strings.HasSuffix(login, "[bot]")
}

// stripBotSuffix removes the "[bot]" suffix from a bot login.
// It returns the login unchanged if no suffix is present.
func stripBotSuffix(login string) string {
	return strings.TrimSuffix(login, "[bot]")
}

// inBotAllowlist reports whether bare (already stripped of "[bot]")
// appears in the allowlist with case-insensitive comparison.
func inBotAllowlist(bare string, allowlist []string) bool {
	for _, allowed := range allowlist {
		if strings.EqualFold(bare, allowed) {
			return true
		}
	}
	return false
}
