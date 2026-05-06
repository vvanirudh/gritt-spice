package github

import (
	"context"
	"fmt"

	"go.abhg.dev/gs/internal/forge"
)

var _ forge.ViewerIdentifier = (*Repository)(nil)

// ViewerLogin returns the GitHub login of the authenticated user.
func (r *Repository) ViewerLogin(ctx context.Context) (string, error) {
	var q struct {
		Viewer struct {
			Login string `graphql:"login"`
		} `graphql:"viewer"`
	}

	if err := r.client.Query(ctx, &q, nil); err != nil {
		return "", fmt.Errorf("query viewer: %w", err)
	}

	return q.Viewer.Login, nil
}
