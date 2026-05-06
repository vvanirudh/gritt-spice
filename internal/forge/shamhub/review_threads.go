package shamhub

import (
	"context"
	"fmt"
	"iter"
	"strconv"
	"strings"
	"time"

	"go.abhg.dev/gs/internal/forge"
)

// Compile-time check that forgeRepository implements ReviewThreadLister.
var _ forge.ReviewThreadLister = (*forgeRepository)(nil)

// shamReviewThread is the internal representation of a review thread.
type shamReviewThread struct {
	ID           int
	Owner        string
	Repo         string
	ChangeNumber int

	File       string
	LineRange  [2]int
	Hunk       string
	Author     string
	Body       string
	IsResolved bool
	URL        string
	Replies    []shamReviewReply
}

// shamReviewReply is a single reply within a review thread.
type shamReviewReply struct {
	ID        int
	Author    string
	Body      string
	CreatedAt time.Time
}

// ReviewThreadInput specifies the fields of a seeded thread for tests.
type ReviewThreadInput struct {
	File       string
	LineRange  [2]int
	Hunk       string
	Author     string
	Body       string
	IsResolved bool
	URL        string
	Replies    []ReviewReplyInput
}

// ReviewReplyInput specifies the fields of a seeded reply for tests.
type ReviewReplyInput struct {
	Author    string
	Body      string
	CreatedAt time.Time
}

// SeedReviewThread inserts a thread under owner/repo/changeNumber.
// Returns the assigned thread ID.
// This is a test-only helper.
func (sh *ShamHub) SeedReviewThread(
	owner, repo string,
	changeNumber int,
	item ReviewThreadInput,
) (forge.ReviewThreadID, error) {
	sh.mu.Lock()
	defer sh.mu.Unlock()

	id := len(sh.reviewThreads) + 1
	replies := make([]shamReviewReply, len(item.Replies))
	for i, r := range item.Replies {
		replies[i] = shamReviewReply{
			ID:        i + 1,
			Author:    r.Author,
			Body:      r.Body,
			CreatedAt: r.CreatedAt,
		}
	}

	sh.reviewThreads = append(sh.reviewThreads, shamReviewThread{
		ID:           id,
		Owner:        owner,
		Repo:         repo,
		ChangeNumber: changeNumber,
		File:         item.File,
		LineRange:    item.LineRange,
		Hunk:         item.Hunk,
		Author:       item.Author,
		Body:         item.Body,
		IsResolved:   item.IsResolved,
		URL:          item.URL,
		Replies:      replies,
	})

	return forge.ReviewThreadID(strconv.Itoa(id)), nil
}

// ListReviewThreadsForTest returns all threads for a change.
// This is a test-only helper.
func (sh *ShamHub) ListReviewThreadsForTest(
	owner, repo string,
	changeNumber int,
) []*forge.ReviewThreadItem {
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	var items []*forge.ReviewThreadItem
	for _, t := range sh.reviewThreads {
		if t.Owner != owner || t.Repo != repo || t.ChangeNumber != changeNumber {
			continue
		}
		items = append(items, toReviewThreadItem(t))
	}
	return items
}

// toReviewThreadItem converts a shamReviewThread to a forge.ReviewThreadItem.
func toReviewThreadItem(t shamReviewThread) *forge.ReviewThreadItem {
	replies := make([]forge.ReviewReply, len(t.Replies))
	for i, r := range t.Replies {
		replies[i] = forge.ReviewReply{
			Author:    r.Author,
			Body:      r.Body,
			CreatedAt: r.CreatedAt,
		}
	}

	return &forge.ReviewThreadItem{
		ID:         forge.ReviewThreadID(strconv.Itoa(t.ID)),
		File:       t.File,
		LineRange:  t.LineRange,
		Hunk:       t.Hunk,
		Author:     t.Author,
		Body:       t.Body,
		IsResolved: t.IsResolved,
		URL:        t.URL,
		Replies:    replies,
	}
}

// Bot filtering helpers.
// These mirror the same logic in internal/forge/github/review_threads.go
// but are kept package-private to shamhub to avoid premature sharing.

// _knownBotLogins maps GitHub login → canonical short name used for
// BotAllowlist matching, for review bots that don't carry the "[bot]"
// suffix and would otherwise pass the bot filter unconditionally.
// Mirrors the github package's _knownBotLogins.
var _knownBotLogins = map[string]string{
	"copilot-pull-request-reviewer": "copilot",
}

// isBot reports whether login is an automated account: either it has
// a "[bot]" suffix or it is a known non-suffixed AI review bot.
func isBot(login string) bool {
	if strings.HasSuffix(login, "[bot]") {
		return true
	}
	_, ok := _knownBotLogins[login]
	return ok
}

// stripBotSuffix returns the canonical short form of a bot login:
// "[bot]" suffix removed for App-style logins, or the configured
// canonical name for known non-suffixed bots. Returns the login
// unchanged when neither pattern matches.
func stripBotSuffix(login string) string {
	if bare, ok := strings.CutSuffix(login, "[bot]"); ok {
		return bare
	}
	if canonical, ok := _knownBotLogins[login]; ok {
		return canonical
	}
	return login
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

// HTTP handler registration.
var (
	_ = shamhubRESTHandler(
		"POST /{owner}/{repo}/changes/{number}/review_threads/list",
		(*ShamHub).handleListReviewThreads,
	)
	_ = shamhubRESTHandler(
		"POST /{owner}/{repo}/review_threads/{id}/replies",
		(*ShamHub).handlePostReviewThreadReply,
	)
)

// listReviewThreadsRequest is the request type for listing review threads.
type listReviewThreadsRequest struct {
	Owner  string `path:"owner" json:"-"`
	Repo   string `path:"repo" json:"-"`
	Number int    `path:"number" json:"-"`

	IncludeResolved bool     `json:"includeResolved,omitempty"`
	BotAllowlist    []string `json:"botAllowlist,omitempty"`
}

// listReviewThreadsResponse is the response type for listing review threads.
type listReviewThreadsResponse struct {
	Items []*listReviewThreadItem `json:"items,omitempty"`
}

// listReviewThreadItem is a single item in a list review threads response.
type listReviewThreadItem struct {
	ID         string                 `json:"id,omitempty"`
	File       string                 `json:"file,omitempty"`
	LineRange  [2]int                 `json:"lineRange,omitempty"`
	Hunk       string                 `json:"hunk,omitempty"`
	Author     string                 `json:"author,omitempty"`
	Body       string                 `json:"body,omitempty"`
	IsResolved bool                   `json:"isResolved,omitempty"`
	URL        string                 `json:"url,omitempty"`
	Replies    []*listReviewReplyItem `json:"replies,omitempty"`
}

// listReviewReplyItem is a single reply in a list review threads response.
type listReviewReplyItem struct {
	Author    string    `json:"author,omitempty"`
	Body      string    `json:"body,omitempty"`
	CreatedAt time.Time `json:"createdAt,omitzero"`
}

// handleListReviewThreads handles
// POST /{owner}/{repo}/changes/{number}/review_threads/list.
// The route uses POST (not GET) because the filter options
// (IncludeResolved, BotAllowlist) are sent as a JSON body, which the
// REST handler machinery only decodes for non-GET requests.
func (sh *ShamHub) handleListReviewThreads(
	_ context.Context,
	req *listReviewThreadsRequest,
) (*listReviewThreadsResponse, error) {
	owner, repo, changeNum := req.Owner, req.Repo, req.Number

	sh.mu.RLock()
	var threads []shamReviewThread
	for _, t := range sh.reviewThreads {
		if t.Owner == owner && t.Repo == repo && t.ChangeNumber == changeNum {
			threads = append(threads, t)
		}
	}
	sh.mu.RUnlock()

	var items []*listReviewThreadItem
	for _, t := range threads {
		// Skip resolved threads unless requested.
		if t.IsResolved && !req.IncludeResolved {
			continue
		}

		// Apply bot filtering.
		if isBot(t.Author) {
			bare := stripBotSuffix(t.Author)
			if !inBotAllowlist(bare, req.BotAllowlist) {
				continue
			}
		}

		replies := make([]*listReviewReplyItem, len(t.Replies))
		for i, r := range t.Replies {
			replies[i] = &listReviewReplyItem{
				Author:    r.Author,
				Body:      r.Body,
				CreatedAt: r.CreatedAt,
			}
		}

		items = append(items, &listReviewThreadItem{
			ID:         strconv.Itoa(t.ID),
			File:       t.File,
			LineRange:  t.LineRange,
			Hunk:       t.Hunk,
			Author:     t.Author,
			Body:       t.Body,
			IsResolved: t.IsResolved,
			URL:        t.URL,
			Replies:    replies,
		})
	}

	return &listReviewThreadsResponse{Items: items}, nil
}

// postReviewThreadReplyRequest is the request type for posting a reply.
type postReviewThreadReplyRequest struct {
	Owner    string `path:"owner" json:"-"`
	Repo     string `path:"repo" json:"-"`
	ThreadID int    `path:"id" json:"-"`

	Author string `json:"author,omitempty"`
	Body   string `json:"body,omitempty"`
}

// postReviewThreadReplyResponse is the response type for posting a reply.
type postReviewThreadReplyResponse struct {
	ID int `json:"id,omitempty"`
}

// handlePostReviewThreadReply handles POST /{owner}/{repo}/review_threads/{id}/replies.
// The lookup is scoped to the {owner}/{repo} from the request path so a
// reply can only be posted to a thread that lives in the named repo,
// even though shamhub thread IDs are globally unique.
func (sh *ShamHub) handlePostReviewThreadReply(
	_ context.Context,
	req *postReviewThreadReplyRequest,
) (*postReviewThreadReplyResponse, error) {
	owner, repo, threadID := req.Owner, req.Repo, req.ThreadID

	sh.mu.Lock()
	defer sh.mu.Unlock()

	var found bool
	var replyID int
	for i, t := range sh.reviewThreads {
		if t.ID != threadID || t.Owner != owner || t.Repo != repo {
			continue
		}

		found = true
		replyID = len(t.Replies) + 1
		sh.reviewThreads[i].Replies = append(
			sh.reviewThreads[i].Replies,
			shamReviewReply{
				ID:        replyID,
				Author:    req.Author,
				Body:      req.Body,
				CreatedAt: time.Now(),
			},
		)
		break
	}

	if !found {
		return nil, notFoundErrorf(
			"review thread %d not found in %s/%s",
			threadID, req.Owner, req.Repo,
		)
	}

	return &postReviewThreadReplyResponse{ID: replyID}, nil
}

// ListReviewThreads returns an iterator over review threads for the given change.
// Threads are filtered according to opts:
// resolved threads are excluded unless IncludeResolved is set,
// and bot-authored threads are excluded unless the bot login
// (with "[bot]" suffix stripped) is in BotAllowlist.
func (r *forgeRepository) ListReviewThreads(
	ctx context.Context,
	id forge.ChangeID,
	opts *forge.ListReviewThreadsOptions,
) iter.Seq2[*forge.ReviewThreadItem, error] {
	if opts == nil {
		opts = &forge.ListReviewThreadsOptions{}
	}

	changeNum := int(id.(ChangeID))
	u := r.apiURL.JoinPath(
		r.owner, r.repo,
		"changes", strconv.Itoa(changeNum),
		"review_threads", "list",
	)

	req := listReviewThreadsRequest{
		IncludeResolved: opts.IncludeResolved,
		BotAllowlist:    opts.BotAllowlist,
	}

	return func(yield func(*forge.ReviewThreadItem, error) bool) {
		var res listReviewThreadsResponse
		if err := r.client.Post(ctx, u.String(), req, &res); err != nil {
			yield(nil, fmt.Errorf("list review threads: %w", err))
			return
		}

		for _, item := range res.Items {
			replies := make([]forge.ReviewReply, len(item.Replies))
			for i, rep := range item.Replies {
				replies[i] = forge.ReviewReply{
					Author:    rep.Author,
					Body:      rep.Body,
					CreatedAt: rep.CreatedAt,
				}
			}

			thread := &forge.ReviewThreadItem{
				ID:         forge.ReviewThreadID(item.ID),
				File:       item.File,
				LineRange:  item.LineRange,
				Hunk:       item.Hunk,
				Author:     item.Author,
				Body:       item.Body,
				IsResolved: item.IsResolved,
				URL:        item.URL,
				Replies:    replies,
			}

			if !yield(thread, nil) {
				return
			}
		}
	}
}

// PostReviewThreadReply posts a reply to an existing review thread
// and returns the ID of the newly created comment.
func (r *forgeRepository) PostReviewThreadReply(
	ctx context.Context,
	threadID forge.ReviewThreadID,
	body string,
) (forge.ChangeCommentID, error) {
	tid, err := strconv.Atoi(string(threadID))
	if err != nil {
		return nil, fmt.Errorf("parse thread ID %q: %w", threadID, err)
	}

	u := r.apiURL.JoinPath(
		r.owner, r.repo,
		"review_threads", strconv.Itoa(tid),
		"replies",
	)
	req := postReviewThreadReplyRequest{Body: body}
	var res postReviewThreadReplyResponse
	if err := r.client.Post(ctx, u.String(), req, &res); err != nil {
		return nil, fmt.Errorf("post review thread reply: %w", err)
	}

	return ChangeCommentID(res.ID), nil
}
