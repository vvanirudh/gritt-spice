package github

import (
	"testing"

	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"go.abhg.dev/gs/internal/forge"
)

func TestForgeReviewDecision(t *testing.T) {
	tests := []struct {
		name    string
		d       githubv4.PullRequestReviewDecision
		logins  []string
		want    forge.ChangeReviewDecision
	}{
		{
			name: "Approved",
			d:    githubv4.PullRequestReviewDecisionApproved,
			want: forge.ChangeReviewApproved,
		},
		{
			name: "ChangesRequested",
			d:    githubv4.PullRequestReviewDecisionChangesRequested,
			want: forge.ChangeReviewChangesRequested,
		},
		{
			name: "ReviewRequired",
			d:    githubv4.PullRequestReviewDecisionReviewRequired,
			want: forge.ChangeReviewRequired,
		},
		{
			name: "NoReviewNoReviewers",
			want: forge.ChangeReviewNoReview,
		},
		{
			name:   "HumanReviewerPending",
			logins: []string{"alice"},
			want:   forge.ChangeReviewRequired,
		},
		{
			name:   "BotOnlyReviewerIgnored",
			logins: []string{"copilot-pull-request-reviewer[bot]"},
			want:   forge.ChangeReviewNoReview,
		},
		{
			name:   "MultipleBotReviewersIgnored",
			logins: []string{"copilot-pull-request-reviewer[bot]", "some-other-bot[bot]"},
			want:   forge.ChangeReviewNoReview,
		},
		{
			name:   "BotAndHumanReviewers",
			logins: []string{"copilot-pull-request-reviewer[bot]", "alice"},
			want:   forge.ChangeReviewRequired,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := forgeReviewDecision(tt.d, tt.logins)
			assert.Equal(t, tt.want, got)
		})
	}
}
