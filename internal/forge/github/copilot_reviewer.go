package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"go.abhg.dev/gs/internal/forge"
)

// _copilotReviewerLogin is the GitHub login string that the REST API
// accepts when requesting Copilot Code Review.
const _copilotReviewerLogin = "copilot-pull-request-reviewer"

// _copilotReviewerAliases lists the logins (case-insensitive)
// that may identify Copilot in API responses.
//
// GitHub may surface Copilot under "Copilot" in some fields
// (e.g., requested_reviewers) and under
// "copilot-pull-request-reviewer" in others (e.g., reviews.user.login,
// the actual bot account).
var _copilotReviewerAliases = []string{
	"copilot",
	_copilotReviewerLogin,
}

var _ forge.CopilotReviewerRequester = (*Repository)(nil)

// RequestCopilotReview adds Copilot as a requested reviewer on the
// given pull request. It is idempotent: if Copilot is already in
// the PR's requested reviewers or has submitted a review,
// it returns (false, nil) without making the request.
//
// This uses GitHub's REST API rather than GraphQL because Copilot is
// a Bot identity and GraphQL's user(login:...) lookup rejects bots
// with NOT_FOUND.
func (r *Repository) RequestCopilotReview(
	ctx context.Context,
	id forge.ChangeID,
) (bool, error) {
	if r.httpClient == nil || r.apiURL == "" {
		return false, errors.New(
			"REST client not configured for this repository",
		)
	}

	prNum := mustPR(id).Number

	// Idempotency check 1: requested reviewers.
	already, err := r.copilotAlreadyRequested(ctx, prNum)
	if err != nil {
		return false, fmt.Errorf("check requested reviewers: %w", err)
	}
	if already {
		return false, nil
	}

	// Idempotency check 2: existing reviews.
	already, err = r.copilotAlreadyReviewed(ctx, prNum)
	if err != nil {
		return false, fmt.Errorf("check reviews: %w", err)
	}
	if already {
		return false, nil
	}

	// POST the request.
	if err := r.requestCopilotReviewer(ctx, prNum); err != nil {
		// Race-loss case: between our idempotency checks and this
		// POST, a concurrent push may have triggered Copilot's
		// auto-review. GitHub then responds with 422 and a body
		// indicating Copilot is already a reviewer. Treat that
		// response as equivalent to the idempotency-hit path:
		// no new request was made, but Copilot is reviewing.
		var statusErr *httpStatusError
		if errors.As(err, &statusErr) &&
			statusErr.StatusCode == http.StatusUnprocessableEntity &&
			strings.Contains(
				strings.ToLower(statusErr.Body),
				"already",
			) {
			return false, nil
		}
		return false, fmt.Errorf("request reviewer: %w", err)
	}
	return true, nil
}

// copilotAlreadyRequested reports whether Copilot is already in the
// requested reviewers list for the given PR.
func (r *Repository) copilotAlreadyRequested(
	ctx context.Context,
	prNum int,
) (bool, error) {
	endpoint := fmt.Sprintf(
		"/repos/%s/%s/pulls/%d/requested_reviewers",
		r.owner, r.repo, prNum,
	)

	var resp struct {
		Users []struct {
			Login string `json:"login"`
		} `json:"users"`
	}
	if err := r.restGet(ctx, endpoint, &resp); err != nil {
		return false, err
	}

	for _, u := range resp.Users {
		if isCopilotLogin(u.Login) {
			return true, nil
		}
	}
	return false, nil
}

// copilotAlreadyReviewed reports whether Copilot has submitted at
// least one review on the given PR.
func (r *Repository) copilotAlreadyReviewed(
	ctx context.Context,
	prNum int,
) (bool, error) {
	endpoint := fmt.Sprintf(
		"/repos/%s/%s/pulls/%d/reviews",
		r.owner, r.repo, prNum,
	)

	var reviews []struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := r.restGet(ctx, endpoint, &reviews); err != nil {
		return false, err
	}

	for _, rev := range reviews {
		if isCopilotLogin(rev.User.Login) {
			return true, nil
		}
	}
	return false, nil
}

// requestCopilotReviewer POSTs the request to add Copilot as a
// reviewer on the given PR.
func (r *Repository) requestCopilotReviewer(
	ctx context.Context,
	prNum int,
) error {
	endpoint := fmt.Sprintf(
		"/repos/%s/%s/pulls/%d/requested_reviewers",
		r.owner, r.repo, prNum,
	)

	body, err := json.Marshal(map[string]any{
		"reviewers": []string{_copilotReviewerLogin},
	})
	if err != nil {
		return fmt.Errorf("marshal body: %w", err)
	}

	return r.restPost(ctx, endpoint, bytes.NewReader(body))
}

// restGet performs an authenticated GET request to the GitHub REST API
// at the given endpoint (path beginning with "/") and decodes the JSON
// response into out.
func (r *Repository) restGet(
	ctx context.Context,
	endpoint string,
	out any,
) error {
	reqURL, err := r.restURL(endpoint)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	setRESTHeaders(req)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return newHTTPStatusError("GET", endpoint, resp)
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// restPost performs an authenticated POST request to the GitHub REST
// API at the given endpoint with the given JSON body.
func (r *Repository) restPost(
	ctx context.Context,
	endpoint string,
	body io.Reader,
) error {
	reqURL, err := r.restURL(endpoint)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	setRESTHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return newHTTPStatusError("POST", endpoint, resp)
	}
	return nil
}

// restURL builds an absolute REST API URL by joining the REST base
// URL (derived from the configured API base URL) with the given
// endpoint path.
func (r *Repository) restURL(endpoint string) (string, error) {
	base, err := restBase(r.apiURL)
	if err != nil {
		return "", fmt.Errorf("derive REST base URL: %w", err)
	}
	u, err := url.JoinPath(base, endpoint)
	if err != nil {
		return "", fmt.Errorf("build URL: %w", err)
	}
	return u, nil
}

// restBase derives the REST API base URL from the configured API base
// URL (which is the GraphQL base, per [repositoryOptions.APIURL]).
//
// On github.com the API base is "https://api.github.com", which serves
// both GraphQL (at /graphql) and REST (at /repos/...) from the same
// host, so the base is returned unchanged.
//
// On GitHub Enterprise the API base conventionally ends in "/api",
// with GraphQL at "/api/graphql" and REST under "/api/v3/...".
// In that case the REST base is "<apiURL>/v3".
func restBase(apiURL string) (string, error) {
	u, err := url.Parse(apiURL)
	if err != nil {
		return "", fmt.Errorf("parse API URL: %w", err)
	}

	// GHE convention: API base path ends in "/api".
	// REST endpoints live under "/api/v3".
	if strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/api") {
		return strings.TrimRight(apiURL, "/") + "/v3", nil
	}
	return apiURL, nil
}

// setRESTHeaders sets the standard headers required by the GitHub
// REST API.
func setRESTHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
}

// httpStatusError describes a non-2xx HTTP response from the REST API.
// It carries the status code and (truncated) response body so that
// callers can make decisions based on either field
// (e.g., distinguishing 422 already-requested from real failures).
type httpStatusError struct {
	Method     string // HTTP method, e.g. "GET" or "POST"
	Endpoint   string // request path
	Status     string // status line, e.g. "422 Unprocessable Entity"
	StatusCode int    // HTTP status code, e.g. 422
	Body       string // response body, truncated to 512 bytes
}

func (e *httpStatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("%s %s: %s", e.Method, e.Endpoint, e.Status)
	}
	return fmt.Sprintf(
		"%s %s: %s: %s",
		e.Method, e.Endpoint, e.Status, e.Body,
	)
}

// newHTTPStatusError builds an [httpStatusError] from an HTTP response,
// reading up to 512 bytes of the body for diagnostics.
func newHTTPStatusError(
	method, endpoint string,
	resp *http.Response,
) *httpStatusError {
	bs, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return &httpStatusError{
		Method:     method,
		Endpoint:   endpoint,
		Status:     resp.Status,
		StatusCode: resp.StatusCode,
		Body:       string(bytes.TrimSpace(bs)),
	}
}

// isCopilotLogin reports whether the given login matches any known
// alias for the Copilot reviewer (case-insensitive).
func isCopilotLogin(login string) bool {
	for _, alias := range _copilotReviewerAliases {
		if strings.EqualFold(login, alias) {
			return true
		}
	}
	return false
}
