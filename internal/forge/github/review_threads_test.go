package github

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.abhg.dev/gs/internal/forge"
	"go.abhg.dev/gs/internal/silog/silogtest"
)

// This file uses httptest-backed mocks rather than the cassette pattern
// in integration_test.go because the logic under test is client-side
// filtering of GraphQL responses (resolved-state, bot allowlist, line-range
// projection); a recorded fixture would not exercise that filtering and
// would couple tests to a specific GitHub PR shape. integration_test.go
// covers the wire-level integration with cassettes.

// threadComment is the JSON representation of a review comment
// as returned by the GitHub GraphQL API in tests.
type threadComment struct {
	Author    map[string]string `json:"author"`
	Body      string            `json:"body"`
	DiffHunk  string            `json:"diffHunk"`
	URL       string            `json:"url"`
	CreatedAt time.Time         `json:"createdAt"`
	StartLine *int              `json:"startLine"`
	Line      *int              `json:"line"`
}

// threadNode is the JSON representation of a review thread
// as returned by the GitHub GraphQL API in tests.
type threadNode struct {
	ID         string         `json:"id"`
	Path       string         `json:"path"`
	IsResolved bool           `json:"isResolved"`
	Comments   map[string]any `json:"comments"`
}

// makeThreadResponse builds the JSON response body for a
// pullRequestReviewThreads query.
func makeThreadResponse(threads []threadNode, hasNextPage bool) map[string]any {
	return map[string]any{
		"data": map[string]any{
			"node": map[string]any{
				"reviewThreads": map[string]any{
					"pageInfo": map[string]any{
						"endCursor":   "cursor1",
						"hasNextPage": hasNextPage,
					},
					"nodes": threads,
				},
			},
		},
	}
}

// newTestRepo creates a Repository backed by the given test HTTP server.
func newTestRepo(t *testing.T, srv *httptest.Server) *Repository {
	t.Helper()
	repo, err := newRepository(
		t.Context(), new(Forge),
		"owner", "repo",
		silogtest.New(t),
		githubv4.NewEnterpriseClient(srv.URL, nil),
		"repoID",
		nil,
	)
	require.NoError(t, err)
	return repo
}

// makeComments wraps a slice of threadComment into the GraphQL
// comments connection structure.
func makeComments(comments []threadComment) map[string]any {
	return map[string]any{"nodes": comments}
}

func intPtr(i int) *int { return &i }

// TestListReviewThreads_resolvedFiltering verifies that resolved threads
// are excluded by default and included when IncludeResolved is set.
func TestListReviewThreads_resolvedFiltering(t *testing.T) {
	threads := []threadNode{
		{
			ID:   "thread1",
			Path: "foo.go",
			Comments: makeComments([]threadComment{
				{
					Author: map[string]string{"login": "alice"},
					Body:   "open thread",
					URL:    "https://example.com/t1",
					Line:   intPtr(10),
				},
			}),
		},
		{
			ID:         "thread2",
			Path:       "bar.go",
			IsResolved: true,
			Comments: makeComments([]threadComment{
				{
					Author: map[string]string{"login": "bob"},
					Body:   "resolved thread",
					URL:    "https://example.com/t2",
					Line:   intPtr(20),
				},
			}),
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		enc := json.NewEncoder(w)
		assert.NoError(t, enc.Encode(makeThreadResponse(threads, false)))
	}))
	defer srv.Close()

	repo := newTestRepo(t, srv)
	prID := &PR{Number: 1, GQLID: "prGQLID"}

	t.Run("DefaultExcludesResolved", func(t *testing.T) {
		var items []*forge.ReviewThreadItem
		for item, err := range repo.ListReviewThreads(t.Context(), prID, nil) {
			require.NoError(t, err)
			items = append(items, item)
		}
		require.Len(t, items, 1)
		assert.Equal(t, "open thread", items[0].Body)
		assert.Equal(t, "alice", items[0].Author)
		assert.Equal(t, "foo.go", items[0].File)
		assert.Equal(t, [2]int{10, 10}, items[0].LineRange)
	})

	t.Run("IncludeResolved", func(t *testing.T) {
		var items []*forge.ReviewThreadItem
		for item, err := range repo.ListReviewThreads(
			t.Context(), prID,
			&forge.ListReviewThreadsOptions{IncludeResolved: true},
		) {
			require.NoError(t, err)
			items = append(items, item)
		}
		require.Len(t, items, 2)
		assert.Equal(t, "open thread", items[0].Body)
		assert.Equal(t, "resolved thread", items[1].Body)
		assert.True(t, items[1].IsResolved)
	})
}

// TestListReviewThreads_botFiltering verifies that bot threads are
// excluded unless their bare login is in BotAllowlist.
func TestListReviewThreads_botFiltering(t *testing.T) {
	threads := []threadNode{
		{
			ID:   "thread1",
			Path: "a.go",
			Comments: makeComments([]threadComment{
				{
					Author: map[string]string{"login": "copilot[bot]"},
					Body:   "copilot comment",
					URL:    "https://example.com/t1",
				},
			}),
		},
		{
			ID:   "thread2",
			Path: "b.go",
			Comments: makeComments([]threadComment{
				{
					Author: map[string]string{"login": "dependabot[bot]"},
					Body:   "dependabot comment",
					URL:    "https://example.com/t2",
				},
			}),
		},
		{
			ID:   "thread3",
			Path: "c.go",
			Comments: makeComments([]threadComment{
				{
					Author: map[string]string{"login": "humanuser"},
					Body:   "human comment",
					URL:    "https://example.com/t3",
				},
			}),
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		enc := json.NewEncoder(w)
		assert.NoError(t, enc.Encode(makeThreadResponse(threads, false)))
	}))
	defer srv.Close()

	repo := newTestRepo(t, srv)
	prID := &PR{Number: 1, GQLID: "prGQLID"}

	t.Run("NoBotAllowlist", func(t *testing.T) {
		// No bots should be included when allowlist is empty.
		var bodies []string
		for item, err := range repo.ListReviewThreads(
			t.Context(), prID,
			&forge.ListReviewThreadsOptions{},
		) {
			require.NoError(t, err)
			bodies = append(bodies, item.Body)
		}
		assert.Equal(t, []string{"human comment"}, bodies)
	})

	t.Run("CopilotAllowed", func(t *testing.T) {
		// With copilot in allowlist, copilot and human threads appear.
		var bodies []string
		for item, err := range repo.ListReviewThreads(
			t.Context(), prID,
			&forge.ListReviewThreadsOptions{
				BotAllowlist: []string{"copilot"},
			},
		) {
			require.NoError(t, err)
			bodies = append(bodies, item.Body)
		}
		assert.Equal(t, []string{"copilot comment", "human comment"}, bodies)
	})

	t.Run("CaseInsensitiveAllowlist", func(t *testing.T) {
		// Allowlist matching is case-insensitive.
		var bodies []string
		for item, err := range repo.ListReviewThreads(
			t.Context(), prID,
			&forge.ListReviewThreadsOptions{
				BotAllowlist: []string{"COPILOT"},
			},
		) {
			require.NoError(t, err)
			bodies = append(bodies, item.Body)
		}
		assert.Equal(t, []string{"copilot comment", "human comment"}, bodies)
	})
}

// TestListReviewThreads_lineRange verifies line range population.
func TestListReviewThreads_lineRange(t *testing.T) {
	threads := []threadNode{
		{
			ID:   "thread1",
			Path: "a.go",
			Comments: makeComments([]threadComment{
				{
					Author:    map[string]string{"login": "alice"},
					Body:      "single line",
					URL:       "https://example.com/t1",
					Line:      intPtr(42),
					StartLine: nil,
				},
			}),
		},
		{
			ID:   "thread2",
			Path: "b.go",
			Comments: makeComments([]threadComment{
				{
					Author:    map[string]string{"login": "alice"},
					Body:      "multi line",
					URL:       "https://example.com/t2",
					StartLine: intPtr(10),
					Line:      intPtr(15),
				},
			}),
		},
		{
			ID:   "thread3",
			Path: "c.go",
			Comments: makeComments([]threadComment{
				{
					Author: map[string]string{"login": "alice"},
					Body:   "no line anchor",
					URL:    "https://example.com/t3",
				},
			}),
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		enc := json.NewEncoder(w)
		assert.NoError(t, enc.Encode(makeThreadResponse(threads, false)))
	}))
	defer srv.Close()

	repo := newTestRepo(t, srv)
	prID := &PR{Number: 1, GQLID: "prGQLID"}

	var items []*forge.ReviewThreadItem
	for item, err := range repo.ListReviewThreads(t.Context(), prID, nil) {
		require.NoError(t, err)
		items = append(items, item)
	}

	require.Len(t, items, 3)
	// Single line: both elements equal line.
	assert.Equal(t, [2]int{42, 42}, items[0].LineRange)
	// Multi line: start and end differ.
	assert.Equal(t, [2]int{10, 15}, items[1].LineRange)
	// No anchor: both zero.
	assert.Equal(t, [2]int{0, 0}, items[2].LineRange)
}

// TestListReviewThreads_replies verifies that subsequent comments
// appear as replies on the item.
func TestListReviewThreads_replies(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	threads := []threadNode{
		{
			ID:   "thread1",
			Path: "a.go",
			Comments: makeComments([]threadComment{
				{
					Author:    map[string]string{"login": "alice"},
					Body:      "original",
					URL:       "https://example.com/t1",
					CreatedAt: now,
				},
				{
					Author:    map[string]string{"login": "bob"},
					Body:      "reply 1",
					URL:       "https://example.com/t1c2",
					CreatedAt: now.Add(time.Hour),
				},
				{
					Author:    map[string]string{"login": "alice"},
					Body:      "reply 2",
					URL:       "https://example.com/t1c3",
					CreatedAt: now.Add(2 * time.Hour),
				},
			}),
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		enc := json.NewEncoder(w)
		assert.NoError(t, enc.Encode(makeThreadResponse(threads, false)))
	}))
	defer srv.Close()

	repo := newTestRepo(t, srv)
	prID := &PR{Number: 1, GQLID: "prGQLID"}

	var items []*forge.ReviewThreadItem
	for item, err := range repo.ListReviewThreads(t.Context(), prID, nil) {
		require.NoError(t, err)
		items = append(items, item)
	}

	require.Len(t, items, 1)
	item := items[0]
	assert.Equal(t, "original", item.Body)
	assert.Equal(t, "alice", item.Author)
	require.Len(t, item.Replies, 2)
	assert.Equal(t, "bob", item.Replies[0].Author)
	assert.Equal(t, "reply 1", item.Replies[0].Body)
	assert.Equal(t, "alice", item.Replies[1].Author)
	assert.Equal(t, "reply 2", item.Replies[1].Body)
}

// TestPostReviewThreadReply verifies that PostReviewThreadReply sends the
// correct GraphQL mutation and returns the expected ChangeCommentID
// on success, and propagates errors when the server reports them.
func TestPostReviewThreadReply(t *testing.T) {
	t.Run("HappyPath", func(t *testing.T) {
		const (
			threadID    = "THREAD_GQL_ID"
			replyBody   = "looks good to me"
			commentGQID = "COMMENT_GQL_ID"
			commentURL  = "https://github.com/owner/repo/pull/1#comment-1"
		)

		var capturedBody string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, err := io.ReadAll(r.Body)
			assert.NoError(t, err)
			capturedBody = string(raw)

			enc := json.NewEncoder(w)
			assert.NoError(t, enc.Encode(map[string]any{
				"data": map[string]any{
					"addPullRequestReviewThreadReply": map[string]any{
						"comment": map[string]any{
							"id":  commentGQID,
							"url": commentURL,
						},
					},
				},
			}))
		}))
		defer srv.Close()

		repo := newTestRepo(t, srv)
		got, err := repo.PostReviewThreadReply(
			t.Context(),
			forge.ReviewThreadID(threadID),
			replyBody,
		)
		require.NoError(t, err)
		require.NotNil(t, got)

		// Verify the returned ChangeCommentID carries the expected GraphQL ID.
		prc, ok := got.(*PRComment)
		require.True(t, ok, "expected *PRComment, got %T", got)
		assert.Equal(t, githubv4.ID(commentGQID), prc.GQLID)

		// Verify the request body contained the mutation and inputs.
		assert.Contains(t, capturedBody,
			"addPullRequestReviewThreadReply",
			"request body must contain the mutation name",
		)
		assert.Contains(t, capturedBody,
			threadID,
			"request body must contain the thread ID",
		)
		assert.True(t,
			strings.Contains(capturedBody, replyBody),
			"request body must contain the reply body text",
		)
	})

	t.Run("GraphQLError", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			enc := json.NewEncoder(w)
			assert.NoError(t, enc.Encode(map[string]any{
				"errors": []map[string]any{
					{"message": "thread not found"},
				},
			}))
		}))
		defer srv.Close()

		repo := newTestRepo(t, srv)
		got, err := repo.PostReviewThreadReply(
			t.Context(),
			forge.ReviewThreadID("NO_SUCH_THREAD"),
			"reply",
		)
		assert.Nil(t, got)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "thread not found")
	})
}

// TestIsBot verifies the isBot helper.
func TestIsBot(t *testing.T) {
	tests := []struct {
		login string
		want  bool
	}{
		{"copilot[bot]", true},
		{"dependabot[bot]", true},
		{"humanuser", false},
		{"[bot]", true},
		{"", false},
		// Known non-suffixed AI review bots.
		{"copilot-pull-request-reviewer", true},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, isBot(tt.login), "login=%q", tt.login)
	}
}

// TestStripBotSuffix verifies the stripBotSuffix helper.
func TestStripBotSuffix(t *testing.T) {
	tests := []struct {
		login string
		want  string
	}{
		{"copilot[bot]", "copilot"},
		{"dependabot[bot]", "dependabot"},
		{"humanuser", "humanuser"},
		{"[bot]", ""},
		// Non-suffixed AI bot canonicalizes to its allowlist key.
		{"copilot-pull-request-reviewer", "copilot"},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, stripBotSuffix(tt.login), "login=%q", tt.login)
	}
}

// TestInBotAllowlist verifies the inBotAllowlist helper.
func TestInBotAllowlist(t *testing.T) {
	allowlist := []string{"copilot", "mybot"}

	tests := []struct {
		bare string
		want bool
	}{
		{"copilot", true},
		{"COPILOT", true},
		{"Copilot", true},
		{"mybot", true},
		{"MYBOT", true},
		{"dependabot", false},
		{"", false},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, inBotAllowlist(tt.bare, allowlist), "bare=%q", tt.bare)
	}
}
