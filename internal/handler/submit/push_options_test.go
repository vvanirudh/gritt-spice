package submit

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.abhg.dev/gs/internal/browser"
	"go.abhg.dev/gs/internal/forge"
	"go.abhg.dev/gs/internal/git"
	"go.abhg.dev/gs/internal/silog/silogtest"
	"go.abhg.dev/gs/internal/spice"
	"go.abhg.dev/gs/internal/spice/state"
	gomock "go.uber.org/mock/gomock"
)

type testSubmitRepo struct {
	peels      map[string]git.Hash
	peelErr    map[string]error
	ancestors  map[string]bool
	isAncCalls int
}

func (r *testSubmitRepo) PeelToCommit(_ context.Context, ref string) (git.Hash, error) {
	if err, ok := r.peelErr[ref]; ok {
		return "", err
	}
	h, ok := r.peels[ref]
	if !ok {
		return "", git.ErrNotExist
	}
	return h, nil
}

func (r *testSubmitRepo) IsAncestor(_ context.Context, ancestor, descendant git.Hash) bool {
	r.isAncCalls++
	return r.ancestors[string(ancestor)+".."+string(descendant)]
}

func (*testSubmitRepo) PeelToTree(context.Context, string) (git.Hash, error) {
	panic("unexpected call to PeelToTree")
}

func (*testSubmitRepo) BranchUpstream(context.Context, string) (string, error) {
	return "", git.ErrNotExist
}

func (*testSubmitRepo) SetBranchUpstream(context.Context, string, string) error {
	panic("unexpected call to SetBranchUpstream")
}

func (*testSubmitRepo) Var(context.Context, string) (string, error) {
	panic("unexpected call to Var")
}

func (*testSubmitRepo) CommitMessageRange(context.Context, string, string) ([]git.CommitMessage, error) {
	panic("unexpected call to CommitMessageRange")
}

func (*testSubmitRepo) RemoteFetchRefspecs(context.Context, string) ([]git.Refspec, error) {
	return []git.Refspec{"+refs/heads/*:refs/remotes/origin/*"}, nil
}

type testSubmitWorktree struct {
	pushes  []git.PushOptions
	pushErr error
}

func (w *testSubmitWorktree) Push(_ context.Context, opts git.PushOptions) error {
	w.pushes = append(w.pushes, opts)
	return w.pushErr
}

type testSubmitStore struct {
	trunk string
}

func (s *testSubmitStore) BeginBranchTx() *state.BranchTx {
	panic("unexpected call to BeginBranchTx")
}

func (s *testSubmitStore) Trunk() string { return s.trunk }

func (*testSubmitStore) LoadPreparedBranch(context.Context, string) (*state.PreparedBranch, error) {
	return nil, nil
}

func (*testSubmitStore) SavePreparedBranch(context.Context, *state.PreparedBranch) error {
	return nil
}

func (*testSubmitStore) ClearPreparedBranch(context.Context, string) error {
	return nil
}

func setupSubmitHandlerTest(
	t *testing.T,
	repo *testSubmitRepo,
	wt *testSubmitWorktree,
	mockService *MockService,
) *Handler {
	t.Helper()

	return &Handler{
		Log:        silogtest.New(t),
		Repository: repo,
		Worktree:   wt,
		Store:      &testSubmitStore{trunk: "main"},
		Service:    mockService,
		Browser:    &browser.Noop{},
		FindRemote: func(context.Context) (string, error) { return "origin", nil },
		OpenRemoteRepository: func(context.Context, string) (forge.Repository, error) {
			panic("unexpected call to OpenRemoteRepository")
		},
	}
}

func TestSubmitBranch_ForceWithLeaseSelection(t *testing.T) {
	t.Run("FastForwardPushDoesNotSetForceWithLease", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockService := NewMockService(ctrl)
		localHead := git.Hash("2222222222222222222222222222222222222222")
		remoteHead := git.Hash("1111111111111111111111111111111111111111")

		mockService.EXPECT().
			VerifyRestacked(gomock.Any(), "feature").
			Return(nil)
		mockService.EXPECT().
			LookupBranch(gomock.Any(), "feature").
			Return(&spice.LookupBranchResponse{
				Base:           "main",
				UpstreamBranch: "feature",
			}, nil)

		repo := &testSubmitRepo{
			peels: map[string]git.Hash{
				"feature":        localHead,
				"origin/feature": remoteHead,
			},
			ancestors: map[string]bool{
				string(remoteHead) + ".." + string(localHead): true,
			},
		}
		wt := &testSubmitWorktree{pushErr: errors.New("push failed")}
		h := setupSubmitHandlerTest(t, repo, wt, mockService)

		_, err := h.submitBranch(t.Context(), "feature", &submitOptions{
			Options: &Options{
				Publish: false,
				Web:     OpenWebNever,
			},
		})
		require.Error(t, err)
		assert.ErrorContains(t, err, "push branch")
		require.Len(t, wt.pushes, 1)
		assert.Empty(t, wt.pushes[0].ForceWithLease)
		assert.Equal(t, 1, repo.isAncCalls)
	})

	t.Run("NonFastForwardPushSetsForceWithLease", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mockService := NewMockService(ctrl)
		localHead := git.Hash("4444444444444444444444444444444444444444")
		remoteHead := git.Hash("3333333333333333333333333333333333333333")

		mockService.EXPECT().
			VerifyRestacked(gomock.Any(), "feature").
			Return(nil)
		mockService.EXPECT().
			LookupBranch(gomock.Any(), "feature").
			Return(&spice.LookupBranchResponse{
				Base:           "main",
				UpstreamBranch: "feature",
			}, nil)

		repo := &testSubmitRepo{
			peels: map[string]git.Hash{
				"feature":        localHead,
				"origin/feature": remoteHead,
			},
			ancestors: map[string]bool{
				string(remoteHead) + ".." + string(localHead): false,
			},
		}
		wt := &testSubmitWorktree{pushErr: errors.New("push failed")}
		h := setupSubmitHandlerTest(t, repo, wt, mockService)

		_, err := h.submitBranch(t.Context(), "feature", &submitOptions{
			Options: &Options{
				Publish: false,
				Web:     OpenWebNever,
			},
		})
		require.Error(t, err)
		assert.ErrorContains(t, err, "push branch")
		require.Len(t, wt.pushes, 1)
		assert.Equal(t, "feature:"+remoteHead.String(), wt.pushes[0].ForceWithLease)
		assert.Equal(t, 1, repo.isAncCalls)
	})
}

var _ GitRepository = (*testSubmitRepo)(nil)
var _ GitWorktree = (*testSubmitWorktree)(nil)
var _ Store = (*testSubmitStore)(nil)
