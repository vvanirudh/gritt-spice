package github_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.abhg.dev/gs/internal/forge/github"
	"go.abhg.dev/gs/internal/silog/silogtest"
)

func TestRepository_ViewerLogin(t *testing.T) {
	rec := newRecorder(t, t.Name())
	ghc := newGitHubClient(rec.GetDefaultClient())
	repo, err := github.NewRepository(
		t.Context(), new(github.Forge), "abhinav", "test-repo",
		silogtest.New(t), ghc, _testRepoID, nil,
	)
	require.NoError(t, err)

	login, err := repo.ViewerLogin(t.Context())
	require.NoError(t, err)
	assert.NotEmpty(t, login)
}
