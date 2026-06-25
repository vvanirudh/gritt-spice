package github

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.abhg.dev/gs/internal/silog/silogtest"
)

// newCopilotTestRepo wires a Repository to an httptest server that
// serves both REST and GraphQL endpoints from the same host. Only the
// REST endpoints used by the Copilot reviewer code are exercised here;
// the GraphQL client is wired so that newRepository can be reused.
func newCopilotTestRepo(t *testing.T, srv *httptest.Server) *Repository {
	t.Helper()
	repo, err := newRepository(
		t.Context(), new(Forge),
		"owner", "repo",
		silogtest.New(t),
		githubv4.NewEnterpriseClient(srv.URL, nil),
		"repoID",
		&repositoryOptions{
			HTTPClient: srv.Client(),
			APIURL:     srv.URL,
		},
	)
	require.NoError(t, err)
	return repo
}

// copilotMux builds an http.ServeMux that responds to the
// requested_reviewers, reviews, and POST requested_reviewers
// endpoints with the given JSON payloads.
type copilotMux struct {
	requestedUsers   []string
	reviewersByLogin []string

	// postedCount tracks the number of POSTs to requested_reviewers.
	postedCount atomic.Int32

	// postedBody captures the body of the most recent POST.
	postedBody []byte
}

func (m *copilotMux) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()

	// GET requested_reviewers / POST requested_reviewers share a path.
	mux.HandleFunc(
		"/repos/owner/repo/pulls/1/requested_reviewers",
		func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				users := make([]map[string]string, 0, len(m.requestedUsers))
				for _, u := range m.requestedUsers {
					users = append(users, map[string]string{"login": u})
				}
				resp := map[string]any{"users": users, "teams": []any{}}
				assert.NoError(t, json.NewEncoder(w).Encode(resp))
			case http.MethodPost:
				m.postedCount.Add(1)
				bs, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				m.postedBody = bs
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{}`))
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
		},
	)

	mux.HandleFunc(
		"/repos/owner/repo/pulls/1/reviews",
		func(w http.ResponseWriter, _ *http.Request) {
			reviews := make([]map[string]any, 0, len(m.reviewersByLogin))
			for _, login := range m.reviewersByLogin {
				reviews = append(reviews, map[string]any{
					"user": map[string]string{"login": login},
				})
			}
			assert.NoError(t, json.NewEncoder(w).Encode(reviews))
		},
	)

	return mux
}

func TestRepository_RequestCopilotReview(t *testing.T) {
	t.Run("AlreadyRequested", func(t *testing.T) {
		m := &copilotMux{
			requestedUsers: []string{"alice", "Copilot"},
		}
		srv := httptest.NewServer(m.handler(t))
		defer srv.Close()

		requested, err := newCopilotTestRepo(t, srv).
			RequestCopilotReview(t.Context(), &PR{Number: 1})
		require.NoError(t, err)
		assert.False(t, requested,
			"already-requested case must report no new request")
		assert.Zero(t, m.postedCount.Load(),
			"already-requested case must not POST")
	})

	t.Run("AlreadyReviewed", func(t *testing.T) {
		m := &copilotMux{
			reviewersByLogin: []string{"copilot-pull-request-reviewer"},
		}
		srv := httptest.NewServer(m.handler(t))
		defer srv.Close()

		requested, err := newCopilotTestRepo(t, srv).
			RequestCopilotReview(t.Context(), &PR{Number: 1})
		require.NoError(t, err)
		assert.False(t, requested,
			"already-reviewed case must report no new request")
		assert.Zero(t, m.postedCount.Load(),
			"already-reviewed case must not POST")
	})

	t.Run("NeitherAddsReviewer", func(t *testing.T) {
		m := &copilotMux{
			requestedUsers:   []string{"alice"},
			reviewersByLogin: []string{"bob"},
		}
		srv := httptest.NewServer(m.handler(t))
		defer srv.Close()

		requested, err := newCopilotTestRepo(t, srv).
			RequestCopilotReview(t.Context(), &PR{Number: 1})
		require.NoError(t, err)
		assert.True(t, requested,
			"must report newly-requested when not already present")
		assert.Equal(t, int32(1), m.postedCount.Load(),
			"must POST exactly once")
		assert.Contains(t, string(m.postedBody),
			"copilot-pull-request-reviewer",
			"POST body must include the Copilot login")
	})

	t.Run("Already422", func(t *testing.T) {
		// Simulate the race-loss case: the idempotency-check GETs
		// return empty (so we fall through to the POST), but the
		// POST returns 422 because a concurrent push already
		// triggered Copilot's auto-review.
		mux := http.NewServeMux()
		var postCount atomic.Int32
		mux.HandleFunc(
			"/repos/owner/repo/pulls/1/requested_reviewers",
			func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					_, _ = w.Write([]byte(
						`{"users": [], "teams": []}`,
					))
				case http.MethodPost:
					postCount.Add(1)
					w.WriteHeader(http.StatusUnprocessableEntity)
					_, _ = w.Write([]byte(`{` +
						`"message":"Reviewers could not be requested",` +
						`"errors":[{"message":"Review cannot be ` +
						`requested from pull request author or ` +
						`reviewer that has already reviewed."}]` +
						`}`))
				default:
					http.Error(
						w, "method not allowed",
						http.StatusMethodNotAllowed,
					)
				}
			},
		)
		mux.HandleFunc(
			"/repos/owner/repo/pulls/1/reviews",
			func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`[]`))
			},
		)
		srv := httptest.NewServer(mux)
		defer srv.Close()

		requested, err := newCopilotTestRepo(t, srv).
			RequestCopilotReview(t.Context(), &PR{Number: 1})
		require.NoError(t, err,
			"422 already-requested must be treated as success")
		assert.False(t, requested,
			"race-loss case must report no new request")
		assert.Equal(t, int32(1), postCount.Load(),
			"POST must have been attempted exactly once")
	})

	t.Run("PostError", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.HandleFunc(
			"/repos/owner/repo/pulls/1/requested_reviewers",
			func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(`{"users": [], "teams": []}`))
					return
				}
				http.Error(w, "boom", http.StatusUnprocessableEntity)
			},
		)
		mux.HandleFunc(
			"/repos/owner/repo/pulls/1/reviews",
			func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`[]`))
			},
		)
		srv := httptest.NewServer(mux)
		defer srv.Close()

		requested, err := newCopilotTestRepo(t, srv).
			RequestCopilotReview(t.Context(), &PR{Number: 1})
		require.Error(t, err)
		assert.False(t, requested)
		assert.Contains(t, err.Error(), "request reviewer")
		assert.Contains(t, strings.ToLower(err.Error()), "boom")
	})
}

func TestRepository_restURL(t *testing.T) {
	tests := []struct {
		name     string
		apiURL   string
		endpoint string
		want     string
	}{
		{
			name:     "GitHubDotCom",
			apiURL:   "https://api.github.com",
			endpoint: "/repos/owner/repo/pulls/1/requested_reviewers",
			want: "https://api.github.com" +
				"/repos/owner/repo/pulls/1/requested_reviewers",
		},
		{
			name:     "Enterprise",
			apiURL:   "https://ghe.example.com/api",
			endpoint: "/repos/owner/repo/pulls/1/requested_reviewers",
			want: "https://ghe.example.com/api/v3" +
				"/repos/owner/repo/pulls/1/requested_reviewers",
		},
		{
			name:     "EnterpriseTrailingSlash",
			apiURL:   "https://ghe.example.com/api/",
			endpoint: "/repos/owner/repo/pulls/1/requested_reviewers",
			want: "https://ghe.example.com/api/v3" +
				"/repos/owner/repo/pulls/1/requested_reviewers",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Repository{apiURL: tt.apiURL}
			got, err := r.restURL(tt.endpoint)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsCopilotLogin(t *testing.T) {
	tests := []struct {
		login string
		want  bool
	}{
		{"copilot", true},
		{"Copilot", true},
		{"COPILOT", true},
		{"copilot-pull-request-reviewer", true},
		{"Copilot-Pull-Request-Reviewer", true},
		{"alice", false},
		{"", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, isCopilotLogin(tt.login),
			"login=%q", tt.login)
	}
}
