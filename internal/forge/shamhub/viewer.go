package shamhub

import (
	"context"
	"fmt"

	"go.abhg.dev/gs/internal/forge"
)

// Compile-time check that forgeRepository implements ViewerIdentifier.
var _ forge.ViewerIdentifier = (*forgeRepository)(nil)

// HTTP handler registration.
var _ = shamhubRESTHandler(
	"GET /viewer",
	(*ShamHub).handleGetViewer,
)

// getViewerRequest is the request type for fetching the viewer login.
// It has no fields because GET handlers carry no body or path parameters.
type getViewerRequest struct{}

// getViewerResponse is the response type for fetching the viewer login.
type getViewerResponse struct {
	Login string `json:"login"`
}

// handleGetViewer handles GET /viewer.
func (sh *ShamHub) handleGetViewer(
	_ context.Context,
	_ getViewerRequest,
) (*getViewerResponse, error) {
	sh.mu.RLock()
	defer sh.mu.RUnlock()
	return &getViewerResponse{Login: sh.viewerLogin}, nil
}

// ViewerLogin returns the login name of the authenticated viewer
// as configured on the ShamHub server.
func (r *forgeRepository) ViewerLogin(ctx context.Context) (string, error) {
	u := r.apiURL.JoinPath("viewer")

	var res getViewerResponse
	if err := r.client.Get(ctx, u.String(), &res); err != nil {
		return "", fmt.Errorf("get viewer login: %w", err)
	}

	return res.Login, nil
}
