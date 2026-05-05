package review_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.abhg.dev/gs/internal/claude"
	"go.abhg.dev/gs/internal/forge"
	"go.abhg.dev/gs/internal/review"
)

// fakeBatchRunner satisfies review.BatchRunner for unit tests.
type fakeBatchRunner struct {
	perItem map[string][]string
	err     error
}

func (f fakeBatchRunner) Run(
	_ context.Context,
	_ string,
) (map[string][]string, []string, error) {
	return f.perItem, nil, f.err
}

// TestBuildBatchInstructions_groupsByFile verifies that items from the
// same file are grouped under the same heading.
func TestBuildBatchInstructions_groupsByFile(t *testing.T) {
	items := []review.ClassifiedItem{
		threadItem("1", "main.go", "fix this", "will fix", "rename x"),
		threadItem("2", "util.go", "fix that", "ack", "extract func"),
		threadItem("3", "main.go", "also here", "on it", "add test"),
	}

	got := review.BuildBatchInstructions(items)

	// Should have both file headings.
	assert.Contains(t, got, "## main.go")
	assert.Contains(t, got, "## util.go")

	// main.go section must appear before util.go (sorted).
	mainIdx := strings.Index(got, "## main.go")
	utilIdx := strings.Index(got, "## util.go")
	assert.Less(t, mainIdx, utilIdx, "files should be sorted alphabetically")

	// Items 1 and 3 should both appear in the main.go block.
	assert.Contains(t, got, "#1")
	assert.Contains(t, got, "#3")
	assert.Contains(t, got, "#2")
}

// TestBuildBatchInstructions_includesMarkerHint verifies that each item
// section includes the Addresses #<id> marker hint so the spawned
// session knows what to put in commit bodies.
func TestBuildBatchInstructions_includesMarkerHint(t *testing.T) {
	items := []review.ClassifiedItem{
		threadItem("abc-42", "foo.go", "rename var", "will rename", "s/foo/bar/"),
	}

	got := review.BuildBatchInstructions(items)

	assert.Contains(t, got, "Addresses #abc-42")
	assert.Contains(t, got, "rename var")    // reviewer body
	assert.Contains(t, got, "s/foo/bar/")    // fix plan
	assert.Contains(t, got, "will rename")   // reply draft
}

// TestRunBatch_postsRepliesForCommittedItems verifies that RunBatch
// posts an "Addressed in" reply for each item the agent committed for.
func TestRunBatch_postsRepliesForCommittedItems(t *testing.T) {
	item1 := threadItem("t1", "a.go", "fix me", "draft1", "plan1")
	item2 := threadItem("t2", "b.go", "fix me too", "draft2", "plan2")

	poster := &fakeReplyPoster{}
	runner := fakeBatchRunner{
		perItem: map[string][]string{
			"t1": {"sha1111111"},
			"t2": {"sha2222222"},
		},
	}
	subjectLookup := func(sha string) (string, error) {
		subjects := map[string]string{
			"sha1111111": "fix a.go issue",
			"sha2222222": "fix b.go issue",
		}
		return subjects[sha], nil
	}

	dir := t.TempDir()
	instructionsPath := filepath.Join(dir, "INSTRUCTIONS.md")

	summary, err := review.RunBatch(
		t.Context(),
		[]review.ClassifiedItem{item1, item2},
		instructionsPath,
		runner,
		poster,
		subjectLookup,
	)

	require.NoError(t, err)
	assert.Equal(t, 2, summary.Addressed)
	assert.Equal(t, 0, summary.Skipped)
	assert.Empty(t, summary.Errors)
	require.Len(t, poster.calls, 2)
	assert.Contains(t, poster.calls[0], "Addressed in sha1111")
	assert.Contains(t, poster.calls[0], "fix a.go issue")
	assert.Contains(t, poster.calls[1], "Addressed in sha2222")
	assert.Contains(t, poster.calls[1], "fix b.go issue")

	// Verify INSTRUCTIONS.md was written.
	data, err := os.ReadFile(instructionsPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "Addresses #t1")
}

// TestRunBatch_skipsItemsWithoutCommits verifies that items the agent
// declined (no commit with their id) are recorded as Skipped.
func TestRunBatch_skipsItemsWithoutCommits(t *testing.T) {
	item1 := threadItem("t1", "a.go", "fix me", "draft1", "plan1")
	item2 := threadItem("t2", "b.go", "fix me too", "draft2", "plan2")

	poster := &fakeReplyPoster{}
	runner := fakeBatchRunner{
		// Only t1 has a commit; t2 was skipped by agent.
		perItem: map[string][]string{
			"t1": {"sha1111111"},
		},
	}

	dir := t.TempDir()
	instructionsPath := filepath.Join(dir, "INSTRUCTIONS.md")

	summary, err := review.RunBatch(
		t.Context(),
		[]review.ClassifiedItem{item1, item2},
		instructionsPath,
		runner,
		poster,
		nil,
	)

	require.NoError(t, err)
	assert.Equal(t, 1, summary.Addressed)
	assert.Equal(t, 1, summary.Skipped)
	assert.Empty(t, summary.Errors)
	require.Len(t, poster.calls, 1)
	assert.Contains(t, poster.calls[0], "t1:")
}

// TestRunBatch_collectsRunnerError verifies that a non-nil error from
// the runner is collected in Errors without aborting reply posting.
func TestRunBatch_collectsRunnerError(t *testing.T) {
	item1 := threadItem("t1", "a.go", "fix me", "draft", "plan")

	poster := &fakeReplyPoster{}
	runner := fakeBatchRunner{
		perItem: map[string][]string{
			"t1": {"sha1111111"},
		},
		err: errors.New("claude crashed"),
	}

	dir := t.TempDir()
	instructionsPath := filepath.Join(dir, "INSTRUCTIONS.md")

	summary, err := review.RunBatch(
		t.Context(),
		[]review.ClassifiedItem{item1},
		instructionsPath,
		runner,
		poster,
		nil,
	)

	// RunBatch should not return an error at the top level; it
	// collects runner errors into summary.Errors.
	require.NoError(t, err)
	require.Len(t, summary.Errors, 1)
	assert.Contains(t, summary.Errors[0], "claude crashed")
	// Despite the error, replies are still posted for items that got commits.
	assert.Equal(t, 1, summary.Addressed)
}

// TestRunBatch_collectsPostReplyError verifies that posting errors are
// collected per-item and do not abort remaining items.
func TestRunBatch_collectsPostReplyError(t *testing.T) {
	item1 := threadItem("t1", "a.go", "fix me", "draft1", "plan1")
	item2 := threadItem("t2", "b.go", "fix me too", "draft2", "plan2")

	poster := &fakeReplyPoster{err: errors.New("network down")}
	runner := fakeBatchRunner{
		perItem: map[string][]string{
			"t1": {"sha1111111"},
			"t2": {"sha2222222"},
		},
	}

	dir := t.TempDir()
	instructionsPath := filepath.Join(dir, "INSTRUCTIONS.md")

	summary, err := review.RunBatch(
		t.Context(),
		[]review.ClassifiedItem{item1, item2},
		instructionsPath,
		runner,
		poster,
		nil,
	)

	require.NoError(t, err)
	// Both items tried; both failed to post reply.
	assert.Equal(t, 0, summary.Addressed)
	require.Len(t, summary.Errors, 2)
	assert.Contains(t, summary.Errors[0], "network down")
	assert.Contains(t, summary.Errors[1], "network down")
}

// TestRunBatch_checkItemsSkipped verifies that items without a
// ReviewThreadItem source (check items) are skipped silently.
func TestRunBatch_checkItemsSkipped(t *testing.T) {
	checkIt := review.ClassifiedItem{
		Item: &claude.Item{
			Kind:  "check",
			Title: "lint-check",
			File:  "main.go",
			Body:  "lint failure",
			// Source is nil — no ReviewThreadItem
		},
		Classification: &claude.Classification{
			Category: "bug",
			FixPlan:  "fix lint",
		},
	}

	poster := &fakeReplyPoster{}
	runner := fakeBatchRunner{perItem: map[string][]string{}}

	dir := t.TempDir()
	instructionsPath := filepath.Join(dir, "INSTRUCTIONS.md")

	summary, err := review.RunBatch(
		t.Context(),
		[]review.ClassifiedItem{checkIt},
		instructionsPath,
		runner,
		poster,
		nil,
	)

	require.NoError(t, err)
	// Check items have no thread to reply to, so they are just skipped.
	assert.Equal(t, 0, summary.Addressed)
	assert.Empty(t, poster.calls)
	assert.Empty(t, summary.Errors)
}

// TestRunBatch_usesLastSHAForMultipleCommits verifies that when an item
// is addressed by multiple commits, the last SHA is used for the reply.
func TestRunBatch_usesLastSHAForMultipleCommits(t *testing.T) {
	item := threadItem("t1", "a.go", "fix me", "draft", "plan")
	poster := &fakeReplyPoster{}
	runner := fakeBatchRunner{
		perItem: map[string][]string{
			"t1": {"sha-first1", "sha-last11"},
		},
	}
	subjectLookup := func(sha string) (string, error) {
		if sha == "sha-last11" {
			return "final fix", nil
		}
		return "intermediate", nil
	}

	dir := t.TempDir()
	instructionsPath := filepath.Join(dir, "INSTRUCTIONS.md")

	summary, err := review.RunBatch(
		t.Context(),
		[]review.ClassifiedItem{item},
		instructionsPath,
		runner,
		poster,
		subjectLookup,
	)

	require.NoError(t, err)
	assert.Equal(t, 1, summary.Addressed)
	require.Len(t, poster.calls, 1)
	// Must use the last SHA (truncated to 7 chars by BuildAddressedReply).
	assert.Contains(t, poster.calls[0], "sha-las")
	assert.NotContains(t, poster.calls[0], "intermediate")
	assert.Contains(t, poster.calls[0], "final fix")
}

// TestBuildBatchInstructions_nilSubjectLookup verifies RunBatch works
// when subjectLookup is nil (subject omitted from reply).
func TestRunBatch_nilSubjectLookup(t *testing.T) {
	item := threadItem("t1", "a.go", "fix", "draft", "plan")
	poster := &fakeReplyPoster{}
	runner := fakeBatchRunner{
		perItem: map[string][]string{"t1": {"deadbeef123"}},
	}

	dir := t.TempDir()
	instructionsPath := filepath.Join(dir, "INSTRUCTIONS.md")

	summary, err := review.RunBatch(
		t.Context(),
		[]review.ClassifiedItem{item},
		instructionsPath,
		runner,
		poster,
		nil, // no subject lookup
	)

	require.NoError(t, err)
	assert.Equal(t, 1, summary.Addressed)
	require.Len(t, poster.calls, 1)
	// Reply should still work, just without subject.
	assert.Contains(t, poster.calls[0], "Addressed in deadbee")
}

// Ensure forge import is used (ReviewThreadID is referenced in threadItem helper).
var _ forge.ReviewThreadID
