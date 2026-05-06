package shamhub

import (
	"context"
	"fmt"
	"io"
	"iter"
	"strconv"
	"strings"
	"time"

	"go.abhg.dev/gs/internal/forge"
)

// Compile-time check that forgeRepository implements ChangeChecksLister.
var _ forge.ChangeChecksLister = (*forgeRepository)(nil)

// shamCheck is the internal representation of a CI check run.
type shamCheck struct {
	ID           int
	Owner        string
	Repo         string
	ChangeNumber int

	Name       string
	Status     string
	Conclusion string
	URL        string
	StartedAt  time.Time
	EndedAt    time.Time
	Log        string
}

// ChangeCheckInput specifies the fields of a seeded check for tests.
type ChangeCheckInput struct {
	Name       string
	Status     string
	Conclusion string
	URL        string
	StartedAt  time.Time
	EndedAt    time.Time
}

// SeedCheck inserts a check run for owner/repo/changeNumber with the given log.
// Returns the assigned check ID.
// This is a test-only helper.
func (sh *ShamHub) SeedCheck(
	owner, repo string,
	changeNumber int,
	item ChangeCheckInput,
	log string,
) (forge.CheckRunID, error) {
	sh.mu.Lock()
	defer sh.mu.Unlock()

	id := len(sh.checks) + 1
	sh.checks = append(sh.checks, shamCheck{
		ID:           id,
		Owner:        owner,
		Repo:         repo,
		ChangeNumber: changeNumber,
		Name:         item.Name,
		Status:       item.Status,
		Conclusion:   item.Conclusion,
		URL:          item.URL,
		StartedAt:    item.StartedAt,
		EndedAt:      item.EndedAt,
		Log:          log,
	})

	return forge.CheckRunID(strconv.Itoa(id)), nil
}

// ListChangeChecksForTest returns all checks for a change.
// This is a test-only helper.
func (sh *ShamHub) ListChangeChecksForTest(
	owner, repo string,
	changeNumber int,
) []*forge.ChangeCheckItem {
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	var items []*forge.ChangeCheckItem
	for _, c := range sh.checks {
		if c.Owner != owner || c.Repo != repo || c.ChangeNumber != changeNumber {
			continue
		}
		items = append(items, toChangeCheckItem(c))
	}
	return items
}

// toChangeCheckItem converts a shamCheck to a forge.ChangeCheckItem.
func toChangeCheckItem(c shamCheck) *forge.ChangeCheckItem {
	return &forge.ChangeCheckItem{
		ID:         forge.CheckRunID(strconv.Itoa(c.ID)),
		Name:       c.Name,
		Status:     c.Status,
		Conclusion: c.Conclusion,
		URL:        c.URL,
		StartedAt:  c.StartedAt,
		EndedAt:    c.EndedAt,
	}
}

// _failingConclusions is the set of conclusions considered "failing".
var _failingConclusions = map[string]bool{
	"failure":         true,
	"timed_out":       true,
	"cancelled":       true,
	"action_required": true,
}

// HTTP handler registration.
var (
	_ = shamhubRESTHandler(
		"POST /{owner}/{repo}/changes/{number}/checks/list",
		(*ShamHub).handleListChecks,
	)
	_ = shamhubRESTHandler(
		"GET /{owner}/{repo}/checks/{run_id}/log",
		(*ShamHub).handleGetCheckLog,
	)
)

// listChecksRequest is the request type for listing check runs.
type listChecksRequest struct {
	Owner  string `path:"owner" json:"-"`
	Repo   string `path:"repo" json:"-"`
	Number int    `path:"number" json:"-"`

	OnlyFailing bool `json:"onlyFailing,omitempty"`
}

// listChecksResponse is the response type for listing check runs.
type listChecksResponse struct {
	Items []*listCheckItem `json:"items,omitempty"`
}

// listCheckItem is a single item in a list checks response.
type listCheckItem struct {
	ID         string    `json:"id,omitempty"`
	Name       string    `json:"name,omitempty"`
	Status     string    `json:"status,omitempty"`
	Conclusion string    `json:"conclusion,omitempty"`
	URL        string    `json:"url,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	EndedAt    time.Time `json:"endedAt,omitzero"`
}

// handleListChecks handles POST /{owner}/{repo}/changes/{number}/checks/list.
func (sh *ShamHub) handleListChecks(
	_ context.Context,
	req *listChecksRequest,
) (*listChecksResponse, error) {
	owner, repo, changeNum := req.Owner, req.Repo, req.Number

	sh.mu.RLock()
	var checks []shamCheck
	for _, c := range sh.checks {
		if c.Owner == owner && c.Repo == repo && c.ChangeNumber == changeNum {
			checks = append(checks, c)
		}
	}
	sh.mu.RUnlock()

	var items []*listCheckItem
	for _, c := range checks {
		if req.OnlyFailing && !_failingConclusions[c.Conclusion] {
			continue
		}

		items = append(items, &listCheckItem{
			ID:         strconv.Itoa(c.ID),
			Name:       c.Name,
			Status:     c.Status,
			Conclusion: c.Conclusion,
			URL:        c.URL,
			StartedAt:  c.StartedAt,
			EndedAt:    c.EndedAt,
		})
	}

	return &listChecksResponse{Items: items}, nil
}

// getCheckLogRequest is the request type for fetching a check run log.
type getCheckLogRequest struct {
	Owner string `path:"owner" json:"-"`
	Repo  string `path:"repo" json:"-"`
	RunID int    `path:"run_id" json:"-"`
}

// getCheckLogResponse is the response type for fetching a check run log.
type getCheckLogResponse struct {
	Log string `json:"log"`
}

// handleGetCheckLog handles GET /{owner}/{repo}/checks/{run_id}/log.
func (sh *ShamHub) handleGetCheckLog(
	_ context.Context,
	req *getCheckLogRequest,
) (*getCheckLogResponse, error) {
	sh.mu.RLock()
	defer sh.mu.RUnlock()

	for _, c := range sh.checks {
		if c.ID != req.RunID {
			continue
		}
		if c.Owner != req.Owner || c.Repo != req.Repo {
			continue
		}
		return &getCheckLogResponse{Log: c.Log}, nil
	}

	return nil, notFoundErrorf(
		"check run %d not found in %s/%s",
		req.RunID, req.Owner, req.Repo,
	)
}

// ListChangeChecks returns an iterator over CI check runs
// associated with the given change.
// When opts is nil, OnlyFailing defaults to true.
// When opts is a zero-value struct, all checks are returned.
func (r *forgeRepository) ListChangeChecks(
	ctx context.Context,
	id forge.ChangeID,
	opts *forge.ListChangeChecksOptions,
) iter.Seq2[*forge.ChangeCheckItem, error] {
	onlyFailing := true // default when opts is nil
	if opts != nil {
		onlyFailing = opts.OnlyFailing
	}

	changeNum := int(id.(ChangeID))
	u := r.apiURL.JoinPath(
		r.owner, r.repo,
		"changes", strconv.Itoa(changeNum),
		"checks", "list",
	)

	req := listChecksRequest{OnlyFailing: onlyFailing}

	return func(yield func(*forge.ChangeCheckItem, error) bool) {
		var res listChecksResponse
		if err := r.client.Post(ctx, u.String(), req, &res); err != nil {
			yield(nil, fmt.Errorf("list change checks: %w", err))
			return
		}

		for _, item := range res.Items {
			check := &forge.ChangeCheckItem{
				ID:         forge.CheckRunID(item.ID),
				Name:       item.Name,
				Status:     item.Status,
				Conclusion: item.Conclusion,
				URL:        item.URL,
				StartedAt:  item.StartedAt,
				EndedAt:    item.EndedAt,
			}

			if !yield(check, nil) {
				return
			}
		}
	}
}

// GetCheckLog fetches the log output for the given check run.
// The caller is responsible for closing the returned reader.
func (r *forgeRepository) GetCheckLog(
	ctx context.Context,
	id forge.CheckRunID,
) (io.ReadCloser, error) {
	runID, err := strconv.Atoi(string(id))
	if err != nil {
		return nil, fmt.Errorf("parse check run ID %q: %w", id, err)
	}

	u := r.apiURL.JoinPath(
		r.owner, r.repo,
		"checks", strconv.Itoa(runID),
		"log",
	)

	var res getCheckLogResponse
	if err := r.client.Get(ctx, u.String(), &res); err != nil {
		return nil, fmt.Errorf("get check log: %w", err)
	}

	return io.NopCloser(strings.NewReader(res.Log)), nil
}
