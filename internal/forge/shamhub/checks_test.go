package shamhub

import (
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.abhg.dev/gs/internal/forge"
	"go.abhg.dev/gs/internal/silog/silogtest"
)

func TestForgeRepository_ListChangeChecks(t *testing.T) {
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

	// Seed a failing check.
	failID, err := sh.SeedCheck("alice", "myrepo", changeNumber, ChangeCheckInput{
		Name:       "CI / build",
		Status:     "completed",
		Conclusion: "failure",
		URL:        "http://example.com/checks/1",
	}, "build failed: exit status 1\n")
	require.NoError(t, err)

	// Seed a successful check.
	_, err = sh.SeedCheck("alice", "myrepo", changeNumber, ChangeCheckInput{
		Name:       "CI / lint",
		Status:     "completed",
		Conclusion: "success",
		URL:        "http://example.com/checks/2",
	}, "all lints passed\n")
	require.NoError(t, err)

	// Seed an in-progress check.
	_, err = sh.SeedCheck("alice", "myrepo", changeNumber, ChangeCheckInput{
		Name:   "CI / test",
		Status: "in_progress",
		URL:    "http://example.com/checks/3",
	}, "")
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

	lister, ok := repo.(forge.ChangeChecksLister)
	require.True(t, ok, "forgeRepository must implement forge.ChangeChecksLister")

	t.Run("DefaultOpts", func(t *testing.T) {
		// nil opts: OnlyFailing defaults to true.
		var items []*forge.ChangeCheckItem
		for item, err := range lister.ListChangeChecks(t.Context(), ChangeID(changeNumber), nil) {
			require.NoError(t, err)
			items = append(items, item)
		}

		require.Len(t, items, 1, "only failing check should be returned")
		assert.Equal(t, failID, items[0].ID)
		assert.Equal(t, "CI / build", items[0].Name)
		assert.Equal(t, "failure", items[0].Conclusion)
	})

	t.Run("ZeroValueOpts", func(t *testing.T) {
		// Zero-value opts: OnlyFailing == false, all checks returned.
		var items []*forge.ChangeCheckItem
		for item, err := range lister.ListChangeChecks(
			t.Context(),
			ChangeID(changeNumber),
			&forge.ListChangeChecksOptions{},
		) {
			require.NoError(t, err)
			items = append(items, item)
		}

		require.Len(t, items, 3, "all checks should be returned")
	})
}

func TestForgeRepository_GetCheckLog(t *testing.T) {
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

	const wantLog = "step 1: done\nstep 2: failed\n"
	checkID, err := sh.SeedCheck("alice", "myrepo", changeNumber, ChangeCheckInput{
		Name:       "CI / build",
		Status:     "completed",
		Conclusion: "failure",
	}, wantLog)
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

	lister := repo.(forge.ChangeChecksLister)

	t.Run("KnownID", func(t *testing.T) {
		rc, err := lister.GetCheckLog(t.Context(), checkID)
		require.NoError(t, err)
		defer func() {
			assert.NoError(t, rc.Close())
		}()

		gotLog, err := io.ReadAll(rc)
		require.NoError(t, err)
		assert.Equal(t, wantLog, string(gotLog))
	})

	t.Run("UnknownID", func(t *testing.T) {
		_, err := lister.GetCheckLog(t.Context(), forge.CheckRunID("999"))
		assert.Error(t, err, "should return error for unknown check run ID")
	})
}
