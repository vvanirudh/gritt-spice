package shamhub

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.abhg.dev/gs/internal/forge"
	"go.abhg.dev/gs/internal/silog/silogtest"
)

func TestForgeRepository_ViewerLogin(t *testing.T) {
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

	vi, ok := repo.(forge.ViewerIdentifier)
	require.True(t, ok, "forgeRepository must implement forge.ViewerIdentifier")

	t.Run("Default", func(t *testing.T) {
		login, err := vi.ViewerLogin(t.Context())
		require.NoError(t, err)
		assert.Equal(t, "test-user", login)
	})

	t.Run("Overridden", func(t *testing.T) {
		sh.SetViewerLogin("bob")
		defer sh.SetViewerLogin("test-user")

		login, err := vi.ViewerLogin(t.Context())
		require.NoError(t, err)
		assert.Equal(t, "bob", login)
	})
}
