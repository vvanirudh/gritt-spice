package github

import (
	"context"
	"fmt"
	"net/http"

	"github.com/shurcooL/githubv4"
	"go.abhg.dev/gs/internal/forge"
	"go.abhg.dev/gs/internal/silog"
)

// Repository is a GitHub repository.
type Repository struct {
	owner, repo string
	repoID      githubv4.ID
	log         *silog.Logger
	client      *githubv4.Client

	// httpClient and apiURL are used for REST API calls.
	// They are optional: if nil, REST-backed operations
	// (such as Copilot review requests) fall back to no-ops
	// or return errors.
	httpClient *http.Client
	apiURL     string

	forge *Forge
}

var _ forge.Repository = (*Repository)(nil)

// repositoryOptions configures optional fields on a Repository.
type repositoryOptions struct {
	// HTTPClient is the HTTP client used for REST API calls (separate
	// from the GraphQL v4 client). It MUST be pre-authenticated with a
	// bearer token — e.g., wrapped via golang.org/x/oauth2 — because
	// setRESTHeaders does NOT add an Authorization header. Passing a
	// plain *http.Client will cause all REST calls to fail with 401.
	//
	// If nil, REST-backed operations (such as Copilot review requests)
	// are unavailable and return errors.
	HTTPClient *http.Client

	// APIURL is the base URL for GitHub API calls. For github.com
	// this is "https://api.github.com", used as-is for both GraphQL
	// (at /graphql) and REST (at /repos/...).
	//
	// For GitHub Enterprise this is the GraphQL base, conventionally
	// ending in "/api" (GraphQL: <APIURL>/graphql,
	// REST: <APIURL>/v3/...). The REST path derivation happens
	// inside restURL — callers pass the GraphQL base.
	APIURL string
}

func newRepository(
	ctx context.Context,
	forge *Forge,
	owner, repo string,
	log *silog.Logger,
	client *githubv4.Client,
	repoID githubv4.ID,
	opt *repositoryOptions,
) (*Repository, error) {
	log = log.With("repo", fmt.Sprintf("%s/%s", owner, repo))
	if repoID == "" || repoID == nil {
		var q struct {
			Repository struct {
				ID githubv4.ID `graphql:"id"`
			} `graphql:"repository(owner: $owner, name: $repo)"`
		}
		if err := client.Query(ctx, &q, map[string]any{
			"owner": githubv4.String(owner),
			"repo":  githubv4.String(repo),
		}); err != nil {
			return nil, fmt.Errorf("get repository ID: %w", err)
		}

		repoID = q.Repository.ID
	}

	if opt == nil {
		opt = &repositoryOptions{}
	}

	return &Repository{
		owner:      owner,
		repo:       repo,
		log:        log,
		client:     client,
		repoID:     repoID,
		httpClient: opt.HTTPClient,
		apiURL:     opt.APIURL,
		forge:      forge,
	}, nil
}

// Forge returns the forge this repository belongs to.
func (r *Repository) Forge() forge.Forge { return r.forge }

// userID looks up a user's GraphQL ID by login.
func (r *Repository) userID(ctx context.Context, login string) (githubv4.ID, error) {
	var query struct {
		User struct {
			ID githubv4.ID `graphql:"id"`
		} `graphql:"user(login: $login)"`
	}

	variables := map[string]any{
		"login": githubv4.String(login),
	}

	if err := r.client.Query(ctx, &query, variables); err != nil {
		return "", fmt.Errorf("query user: %w", err)
	}

	id := query.User.ID
	if id == "" || id == nil {
		return "", fmt.Errorf("user not found: %q", login)
	}

	return id, nil
}
