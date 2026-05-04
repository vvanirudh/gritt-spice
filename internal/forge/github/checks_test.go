package github

// This file uses httptest-backed mocks rather than the cassette pattern
// in integration_test.go because the logic under test is client-side
// filtering of GraphQL responses (OnlyFailing flag, status/conclusion
// lowercasing); a recorded fixture would not exercise that filtering and
// would couple tests to a specific GitHub PR shape.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.abhg.dev/gs/internal/forge"
)

// checkRunNode is the JSON representation of a check run
// as returned by the GitHub GraphQL API in tests.
type checkRunNode struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Status      string  `json:"status"`
	Conclusion  *string `json:"conclusion"`
	URL         string  `json:"url"`
	StartedAt   *string `json:"startedAt"`
	CompletedAt *string `json:"completedAt"`
}

// makeChecksResponse builds the JSON response body for a checks query.
// suites is a slice of check run slices (one per suite).
func makeChecksResponse(suites [][]checkRunNode) map[string]any {
	suiteNodes := make([]map[string]any, len(suites))
	for i, runs := range suites {
		suiteNodes[i] = map[string]any{
			"checkRuns": map[string]any{
				"nodes": runs,
			},
		}
	}
	return map[string]any{
		"data": map[string]any{
			"node": map[string]any{
				"commits": map[string]any{
					"nodes": []map[string]any{
						{
							"commit": map[string]any{
								"checkSuites": map[string]any{
									"nodes": suiteNodes,
								},
							},
						},
					},
				},
			},
		},
	}
}

// strPtr returns a pointer to the given string.
func strPtr(s string) *string { return &s }

// TestListChangeChecks_onlyFailingDefault verifies that when opts is nil,
// only failing check runs are returned (OnlyFailing defaults to true).
func TestListChangeChecks_onlyFailingDefault(t *testing.T) {
	runs := []checkRunNode{
		{ID: "run1", Name: "CI / build", Status: "COMPLETED", Conclusion: strPtr("SUCCESS"), URL: "https://example.com/1"},
		{ID: "run2", Name: "CI / test", Status: "COMPLETED", Conclusion: strPtr("FAILURE"), URL: "https://example.com/2"},
		{ID: "run3", Name: "CI / lint", Status: "COMPLETED", Conclusion: strPtr("SUCCESS"), URL: "https://example.com/3"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		enc := json.NewEncoder(w)
		assert.NoError(t, enc.Encode(makeChecksResponse([][]checkRunNode{runs})))
	}))
	defer srv.Close()

	repo := newTestRepo(t, srv)
	prID := &PR{Number: 1, GQLID: "prGQLID"}

	var items []*forge.ChangeCheckItem
	for item, err := range repo.ListChangeChecks(t.Context(), prID, nil) {
		require.NoError(t, err)
		items = append(items, item)
	}

	// Only the failing run should be returned.
	require.Len(t, items, 1)
	assert.Equal(t, "CI / test", items[0].Name)
	assert.Equal(t, "failure", items[0].Conclusion)
	assert.Equal(t, "completed", items[0].Status)
}

// TestListChangeChecks_onlyFailingTrue verifies that when OnlyFailing is true,
// only failed runs are returned from a mixed set.
func TestListChangeChecks_onlyFailingTrue(t *testing.T) {
	runs := []checkRunNode{
		{ID: "run1", Name: "build", Status: "COMPLETED", Conclusion: strPtr("SUCCESS"), URL: "https://example.com/1"},
		{ID: "run2", Name: "test", Status: "COMPLETED", Conclusion: strPtr("FAILURE"), URL: "https://example.com/2"},
		{ID: "run3", Name: "timeout-check", Status: "COMPLETED", Conclusion: strPtr("TIMED_OUT"), URL: "https://example.com/3"},
		{ID: "run4", Name: "cancelled-check", Status: "COMPLETED", Conclusion: strPtr("CANCELLED"), URL: "https://example.com/4"},
		{ID: "run5", Name: "action-required", Status: "COMPLETED", Conclusion: strPtr("ACTION_REQUIRED"), URL: "https://example.com/5"},
		{ID: "run6", Name: "skipped", Status: "COMPLETED", Conclusion: strPtr("SKIPPED"), URL: "https://example.com/6"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		enc := json.NewEncoder(w)
		assert.NoError(t, enc.Encode(makeChecksResponse([][]checkRunNode{runs})))
	}))
	defer srv.Close()

	repo := newTestRepo(t, srv)
	prID := &PR{Number: 1, GQLID: "prGQLID"}

	var items []*forge.ChangeCheckItem
	for item, err := range repo.ListChangeChecks(
		t.Context(), prID,
		&forge.ListChangeChecksOptions{OnlyFailing: true},
	) {
		require.NoError(t, err)
		items = append(items, item)
	}

	// Only failure, timed_out, cancelled, action_required should appear.
	require.Len(t, items, 4)
	names := make([]string, len(items))
	for i, it := range items {
		names[i] = it.Name
	}
	assert.Equal(t, []string{"test", "timeout-check", "cancelled-check", "action-required"}, names)

	// Conclusions should be lowercased.
	assert.Equal(t, "failure", items[0].Conclusion)
	assert.Equal(t, "timed_out", items[1].Conclusion)
	assert.Equal(t, "cancelled", items[2].Conclusion)
	assert.Equal(t, "action_required", items[3].Conclusion)
}

// TestListChangeChecks_includeAll verifies that when OnlyFailing is false
// (zero-value opts), all check runs are returned.
func TestListChangeChecks_includeAll(t *testing.T) {
	runs := []checkRunNode{
		{ID: "run1", Name: "build", Status: "COMPLETED", Conclusion: strPtr("SUCCESS"), URL: "https://example.com/1"},
		{ID: "run2", Name: "test", Status: "COMPLETED", Conclusion: strPtr("FAILURE"), URL: "https://example.com/2"},
		{ID: "run3", Name: "lint", Status: "IN_PROGRESS", URL: "https://example.com/3"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		enc := json.NewEncoder(w)
		assert.NoError(t, enc.Encode(makeChecksResponse([][]checkRunNode{runs})))
	}))
	defer srv.Close()

	repo := newTestRepo(t, srv)
	prID := &PR{Number: 1, GQLID: "prGQLID"}

	var items []*forge.ChangeCheckItem
	// Zero-value opts: OnlyFailing == false → include all.
	for item, err := range repo.ListChangeChecks(
		t.Context(), prID,
		&forge.ListChangeChecksOptions{},
	) {
		require.NoError(t, err)
		items = append(items, item)
	}

	require.Len(t, items, 3)
	assert.Equal(t, "build", items[0].Name)
	assert.Equal(t, "success", items[0].Conclusion)
	assert.Equal(t, "completed", items[0].Status)

	assert.Equal(t, "test", items[1].Name)
	assert.Equal(t, "failure", items[1].Conclusion)

	// In-progress run has no conclusion.
	assert.Equal(t, "lint", items[2].Name)
	assert.Equal(t, "", items[2].Conclusion)
	assert.Equal(t, "in_progress", items[2].Status)
}

// TestListChangeChecks_pagination verifies that runs across multiple suites
// are all yielded (both nested loops are walked).
func TestListChangeChecks_pagination(t *testing.T) {
	suite1 := []checkRunNode{
		{ID: "run1", Name: "suite1-build", Status: "COMPLETED", Conclusion: strPtr("SUCCESS"), URL: "https://example.com/1"},
		{ID: "run2", Name: "suite1-test", Status: "COMPLETED", Conclusion: strPtr("FAILURE"), URL: "https://example.com/2"},
	}
	suite2 := []checkRunNode{
		{ID: "run3", Name: "suite2-deploy", Status: "COMPLETED", Conclusion: strPtr("SUCCESS"), URL: "https://example.com/3"},
		{ID: "run4", Name: "suite2-smoke", Status: "COMPLETED", Conclusion: strPtr("TIMED_OUT"), URL: "https://example.com/4"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		enc := json.NewEncoder(w)
		assert.NoError(t, enc.Encode(makeChecksResponse([][]checkRunNode{suite1, suite2})))
	}))
	defer srv.Close()

	repo := newTestRepo(t, srv)
	prID := &PR{Number: 1, GQLID: "prGQLID"}

	var items []*forge.ChangeCheckItem
	// Include all so we see both suites.
	for item, err := range repo.ListChangeChecks(
		t.Context(), prID,
		&forge.ListChangeChecksOptions{},
	) {
		require.NoError(t, err)
		items = append(items, item)
	}

	// 2 from suite1 + 2 from suite2.
	require.Len(t, items, 4)
	names := make([]string, len(items))
	for i, it := range items {
		names[i] = it.Name
	}
	assert.Equal(t, []string{
		"suite1-build", "suite1-test",
		"suite2-deploy", "suite2-smoke",
	}, names)
}

// TestListChangeChecks_statusAndTimestamps verifies that status/conclusion
// are lowercased and that timestamps are populated correctly.
func TestListChangeChecks_statusAndTimestamps(t *testing.T) {
	startTime := time.Date(2024, 3, 1, 10, 0, 0, 0, time.UTC)
	endTime := time.Date(2024, 3, 1, 10, 5, 0, 0, time.UTC)

	runs := []checkRunNode{
		{
			ID:          "run1",
			Name:        "build",
			Status:      "COMPLETED",
			Conclusion:  strPtr("FAILURE"),
			URL:         "https://example.com/1",
			StartedAt:   strPtr(startTime.Format(time.RFC3339)),
			CompletedAt: strPtr(endTime.Format(time.RFC3339)),
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		enc := json.NewEncoder(w)
		assert.NoError(t, enc.Encode(makeChecksResponse([][]checkRunNode{runs})))
	}))
	defer srv.Close()

	repo := newTestRepo(t, srv)
	prID := &PR{Number: 1, GQLID: "prGQLID"}

	var items []*forge.ChangeCheckItem
	for item, err := range repo.ListChangeChecks(
		t.Context(), prID,
		&forge.ListChangeChecksOptions{},
	) {
		require.NoError(t, err)
		items = append(items, item)
	}

	require.Len(t, items, 1)
	assert.Equal(t, "completed", items[0].Status)
	assert.Equal(t, "failure", items[0].Conclusion)
	assert.Equal(t, startTime, items[0].StartedAt)
	assert.Equal(t, endTime, items[0].EndedAt)
}

// TestListChangeChecks_emptyCommits verifies that no items are yielded
// when the PR has no commits.
func TestListChangeChecks_emptyCommits(t *testing.T) {
	// Response with empty commits array.
	emptyResponse := map[string]any{
		"data": map[string]any{
			"node": map[string]any{
				"commits": map[string]any{
					"nodes": []map[string]any{},
				},
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		enc := json.NewEncoder(w)
		assert.NoError(t, enc.Encode(emptyResponse))
	}))
	defer srv.Close()

	repo := newTestRepo(t, srv)
	prID := &PR{Number: 1, GQLID: "prGQLID"}

	var items []*forge.ChangeCheckItem
	for item, err := range repo.ListChangeChecks(t.Context(), prID, nil) {
		require.NoError(t, err)
		items = append(items, item)
	}

	// No items should be yielded.
	assert.Empty(t, items)
}

// TestGetCheckLog verifies that GetCheckLog returns the documented
// "not supported" error with the run ID embedded.
func TestGetCheckLog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// This handler should never be called for GetCheckLog.
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	repo := newTestRepo(t, srv)
	rc, err := repo.GetCheckLog(t.Context(), "run-id-42")
	assert.Nil(t, rc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not yet supported")
	assert.Contains(t, err.Error(), "run-id-42")
}
