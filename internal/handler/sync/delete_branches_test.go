package sync

import (
	"context"
	"iter"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.abhg.dev/gs/internal/git"
	"go.abhg.dev/gs/internal/handler/delete"
	"go.abhg.dev/gs/internal/silog/silogtest"
)

type testSyncRepo struct {
	branches []git.LocalBranch
}

func (r *testSyncRepo) PeelToCommit(context.Context, string) (git.Hash, error) {
	return "", git.ErrNotExist
}

func (r *testSyncRepo) LocalBranches(_ context.Context, _ *git.LocalBranchesOptions) iter.Seq2[git.LocalBranch, error] {
	return func(yield func(git.LocalBranch, error) bool) {
		for _, b := range r.branches {
			if !yield(b, nil) {
				return
			}
		}
	}
}

func (*testSyncRepo) OpenWorktree(context.Context, string) (*git.Worktree, error) {
	panic("unexpected call to OpenWorktree")
}

func (*testSyncRepo) IsAncestor(context.Context, git.Hash, git.Hash) bool {
	panic("unexpected call to IsAncestor")
}

func (*testSyncRepo) Fetch(context.Context, git.FetchOptions) error {
	panic("unexpected call to Fetch")
}

func (*testSyncRepo) CountCommits(context.Context, git.CommitRange) (int, error) {
	panic("unexpected call to CountCommits")
}

func (*testSyncRepo) DeleteBranch(context.Context, string, git.BranchDeleteOptions) error {
	panic("unexpected call to DeleteBranch")
}

func (*testSyncRepo) RemoteURL(context.Context, string) (string, error) {
	panic("unexpected call to RemoteURL")
}

type testSyncWorktree struct{}

func (*testSyncWorktree) CurrentBranch(context.Context) (string, error) {
	panic("unexpected call to CurrentBranch")
}

func (*testSyncWorktree) Pull(context.Context, git.PullOptions) error {
	panic("unexpected call to Pull")
}

func (*testSyncWorktree) CheckoutBranch(context.Context, string) error {
	panic("unexpected call to CheckoutBranch")
}

func (*testSyncWorktree) RootDir() string { return "/wt" }

type testSyncDelete struct {
	req *delete.Request
}

func (d *testSyncDelete) DeleteBranches(_ context.Context, req *delete.Request) error {
	d.req = req
	return nil
}

func TestDeleteBranches_PropagatesSkipRebaseFlag(t *testing.T) {
	tests := []struct {
		name       string
		skipRebase bool
	}{
		{name: "Enabled", skipRebase: true},
		{name: "Disabled", skipRebase: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			del := &testSyncDelete{}
			h := &Handler{
				Log:                silogtest.New(t),
				Repository:         &testSyncRepo{branches: []git.LocalBranch{{Name: "feature"}}},
				Worktree:           &testSyncWorktree{},
				Delete:             del,
				Remote:             "origin",
				SkipRebaseOnDelete: tt.skipRebase,
			}

			err := h.deleteBranches(t.Context(), []branchDeletion{
				{BranchName: "feature", UpstreamName: "feature"},
			})
			require.NoError(t, err)
			require.NotNil(t, del.req)
			assert.Equal(t, []string{"feature"}, del.req.Branches)
			assert.True(t, del.req.Force)
			assert.Equal(t, tt.skipRebase, del.req.SkipRebase)
		})
	}
}

var _ GitRepository = (*testSyncRepo)(nil)
var _ GitWorktree = (*testSyncWorktree)(nil)
var _ DeleteHandler = (*testSyncDelete)(nil)
