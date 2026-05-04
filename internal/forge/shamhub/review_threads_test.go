package shamhub

import (
	"net/http"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.abhg.dev/gs/internal/forge"
	"go.abhg.dev/gs/internal/silog/silogtest"
)

func TestForgeRepository_ListReviewThreads(t *testing.T) {
	sh, err := New(Config{Log: silogtest.New(t)})
	require.NoError(t, err)
	t.Cleanup(func() {
		assert.NoError(t, sh.Close())
	})

	_, err = sh.NewRepository("alice", "myrepo")
	require.NoError(t, err)

	require.NoError(t, sh.RegisterUser("alice"))
	token, err := sh.IssueToken("alice")
	require.NoError(t, err)

	// Set up a change.
	changeNumber := 1
	sh.mu.Lock()
	sh.changes = append(sh.changes, shamChange{
		Number: changeNumber,
		Base: &shamBranch{
			Owner: "alice",
			Repo:  "myrepo",
			Name:  "main",
		},
		Head: &shamBranch{
			Owner: "alice",
			Repo:  "myrepo",
			Name:  "feature",
		},
	})
	sh.mu.Unlock()

	// Seed an open thread.
	openID, err := sh.SeedReviewThread("alice", "myrepo", changeNumber, ReviewThreadInput{
		File:      "foo.go",
		LineRange:  [2]int{10, 20},
		Hunk:      "@@ -10,5 +10,5 @@",
		Author:    "reviewer",
		Body:      "Please refactor this",
		IsResolved: false,
		URL:       "http://example.com/thread/1",
	})
	require.NoError(t, err)

	// Seed a resolved thread.
	_, err = sh.SeedReviewThread("alice", "myrepo", changeNumber, ReviewThreadInput{
		File:      "bar.go",
		LineRange:  [2]int{5, 5},
		Author:    "reviewer",
		Body:      "Good fix",
		IsResolved: true,
	})
	require.NoError(t, err)

	// Seed a bot thread.
	_, err = sh.SeedReviewThread("alice", "myrepo", changeNumber, ReviewThreadInput{
		File:   "baz.go",
		Author: "copilot[bot]",
		Body:   "Auto-generated suggestion",
	})
	require.NoError(t, err)

	// Open the forge repository.
	shamForge := &Forge{
		Options: Options{
			URL:    sh.GitURL(),
			APIURL: sh.APIURL(),
		},
		Log: silogtest.New(t),
	}
	repo, err := newRepository(
		shamForge,
		&AuthenticationToken{tok: token},
		&RepositoryID{
			url:   sh.GitURL(),
			owner: "alice",
			repo:  "myrepo",
		},
		http.DefaultClient,
	)
	require.NoError(t, err)

	lister, ok := repo.(forge.ReviewThreadLister)
	require.True(t, ok, "forgeRepository must implement forge.ReviewThreadLister")

	t.Run("DefaultOpts", func(t *testing.T) {
		// Default opts: exclude resolved, exclude bots.
		var items []*forge.ReviewThreadItem
		for item, err := range lister.ListReviewThreads(t.Context(), ChangeID(changeNumber), nil) {
			require.NoError(t, err)
			items = append(items, item)
		}

		require.Len(t, items, 1, "only open non-bot thread should be returned")
		assert.Equal(t, openID, items[0].ID)
		assert.Equal(t, "foo.go", items[0].File)
		assert.Equal(t, "reviewer", items[0].Author)
		assert.Equal(t, "Please refactor this", items[0].Body)
		assert.False(t, items[0].IsResolved)
	})

	t.Run("IncludeResolved", func(t *testing.T) {
		opts := &forge.ListReviewThreadsOptions{IncludeResolved: true}
		var items []*forge.ReviewThreadItem
		for item, err := range lister.ListReviewThreads(t.Context(), ChangeID(changeNumber), opts) {
			require.NoError(t, err)
			items = append(items, item)
		}

		// Open + resolved, no bot.
		require.Len(t, items, 2)
		ids := make([]forge.ReviewThreadID, len(items))
		for i, it := range items {
			ids[i] = it.ID
		}
		assert.True(t, slices.Contains(ids, openID), "open thread should be included")
	})

	t.Run("BotAllowlist", func(t *testing.T) {
		opts := &forge.ListReviewThreadsOptions{
			BotAllowlist: []string{"copilot"},
		}
		var items []*forge.ReviewThreadItem
		for item, err := range lister.ListReviewThreads(t.Context(), ChangeID(changeNumber), opts) {
			require.NoError(t, err)
			items = append(items, item)
		}

		// Open human thread + allowed bot thread.
		require.Len(t, items, 2)
		authors := make([]string, len(items))
		for i, it := range items {
			authors[i] = it.Author
		}
		assert.True(t, slices.Contains(authors, "copilot[bot]"), "copilot bot thread should be included")
	})
}

func TestForgeRepository_PostReviewThreadReply(t *testing.T) {
	sh, err := New(Config{Log: silogtest.New(t)})
	require.NoError(t, err)
	t.Cleanup(func() {
		assert.NoError(t, sh.Close())
	})

	_, err = sh.NewRepository("alice", "myrepo")
	require.NoError(t, err)

	require.NoError(t, sh.RegisterUser("alice"))
	token, err := sh.IssueToken("alice")
	require.NoError(t, err)

	changeNumber := 1
	sh.mu.Lock()
	sh.changes = append(sh.changes, shamChange{
		Number: changeNumber,
		Base: &shamBranch{
			Owner: "alice",
			Repo:  "myrepo",
			Name:  "main",
		},
		Head: &shamBranch{
			Owner: "alice",
			Repo:  "myrepo",
			Name:  "feature",
		},
	})
	sh.mu.Unlock()

	threadID, err := sh.SeedReviewThread("alice", "myrepo", changeNumber, ReviewThreadInput{
		File:   "main.go",
		Author: "reviewer",
		Body:   "What does this do?",
	})
	require.NoError(t, err)

	shamForge := &Forge{
		Options: Options{
			URL:    sh.GitURL(),
			APIURL: sh.APIURL(),
		},
		Log: silogtest.New(t),
	}
	repo, err := newRepository(
		shamForge,
		&AuthenticationToken{tok: token},
		&RepositoryID{
			url:   sh.GitURL(),
			owner: "alice",
			repo:  "myrepo",
		},
		http.DefaultClient,
	)
	require.NoError(t, err)

	lister, ok := repo.(forge.ReviewThreadLister)
	require.True(t, ok, "forgeRepository must implement forge.ReviewThreadLister")

	// Post a reply.
	commentID, err := lister.PostReviewThreadReply(
		t.Context(),
		threadID,
		"This computes the sum.",
	)
	require.NoError(t, err)
	assert.NotNil(t, commentID)

	// Verify the reply appears in a subsequent listing.
	opts := &forge.ListReviewThreadsOptions{IncludeResolved: false}
	var items []*forge.ReviewThreadItem
	for item, err := range lister.ListReviewThreads(t.Context(), ChangeID(changeNumber), opts) {
		require.NoError(t, err)
		items = append(items, item)
	}

	require.Len(t, items, 1)
	require.Len(t, items[0].Replies, 1, "reply should appear in the thread")
	assert.Equal(t, "This computes the sum.", items[0].Replies[0].Body)
}

func TestForgeRepository_PostReviewThreadReply_notFound(t *testing.T) {
	sh, err := New(Config{Log: silogtest.New(t)})
	require.NoError(t, err)
	t.Cleanup(func() {
		assert.NoError(t, sh.Close())
	})

	_, err = sh.NewRepository("alice", "myrepo")
	require.NoError(t, err)

	require.NoError(t, sh.RegisterUser("alice"))
	token, err := sh.IssueToken("alice")
	require.NoError(t, err)

	shamForge := &Forge{
		Options: Options{
			URL:    sh.GitURL(),
			APIURL: sh.APIURL(),
		},
		Log: silogtest.New(t),
	}
	repo, err := newRepository(
		shamForge,
		&AuthenticationToken{tok: token},
		&RepositoryID{
			url:   sh.GitURL(),
			owner: "alice",
			repo:  "myrepo",
		},
		http.DefaultClient,
	)
	require.NoError(t, err)

	lister := repo.(forge.ReviewThreadLister)

	_, err = lister.PostReviewThreadReply(
		t.Context(),
		forge.ReviewThreadID("999"),
		"reply body",
	)
	assert.Error(t, err, "should return error for unknown thread ID")
}
