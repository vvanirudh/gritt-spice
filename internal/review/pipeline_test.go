package review_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.abhg.dev/gs/internal/claude"
	"go.abhg.dev/gs/internal/forge"
	"go.abhg.dev/gs/internal/review"
)

// fakeClassifier is a simple classifier returning a fixed response or error.
type fakeClassifier struct {
	resp *claude.Classification
	err  error
}

func (f fakeClassifier) Classify(
	_ context.Context,
	_ *claude.Item,
) (*claude.Classification, error) {
	return f.resp, f.err
}

// countingClassifier tracks in-flight goroutines and the observed peak.
type countingClassifier struct {
	inFlight atomic.Int64
	peak     atomic.Int64
	resp     *claude.Classification
}

func (c *countingClassifier) Classify(
	_ context.Context,
	_ *claude.Item,
) (*claude.Classification, error) {
	cur := c.inFlight.Add(1)
	for {
		old := c.peak.Load()
		if cur <= old || c.peak.CompareAndSwap(old, cur) {
			break
		}
	}
	time.Sleep(5 * time.Millisecond)
	c.inFlight.Add(-1)
	return c.resp, nil
}

func TestPipelineForThreads_filtersAddressedAndDeferred(t *testing.T) {
	viewerLogin := "alice"
	cl := fakeClassifier{
		resp: &claude.Classification{Category: "nit"},
	}

	// Thread 1: deferred — should be skipped.
	threadDeferred := &forge.ReviewThreadItem{
		ID:   "thread-1",
		Body: "fix this nit",
	}

	// Thread 2: already addressed — should be skipped.
	threadAddressed := &forge.ReviewThreadItem{
		ID:   "thread-2",
		Body: "another comment",
		Replies: []forge.ReviewReply{
			{Author: viewerLogin, Body: "Addressed in abc1234"},
		},
	}

	// Thread 3: active — should be classified.
	threadActive := &forge.ReviewThreadItem{
		ID:   "thread-3",
		Body: "please fix this",
	}

	threads := []*forge.ReviewThreadItem{
		threadDeferred,
		threadAddressed,
		threadActive,
	}
	deferred := []forge.ReviewThreadID{"thread-1"}

	results, err := review.PipelineForThreads(
		t.Context(),
		threads,
		deferred,
		viewerLogin,
		cl,
		1,
	)

	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, threadActive, results[0].Item.Source)
	assert.Equal(t, "nit", results[0].Classification.Category)
}

func TestPipelineForThreads_emptyResult(t *testing.T) {
	viewerLogin := "alice"
	cl := fakeClassifier{
		resp: &claude.Classification{Category: "nit"},
	}

	// All threads are deferred.
	threads := []*forge.ReviewThreadItem{
		{ID: "thread-1", Body: "comment"},
		{ID: "thread-2", Body: "another comment"},
	}
	deferred := []forge.ReviewThreadID{"thread-1", "thread-2"}

	results, err := review.PipelineForThreads(
		t.Context(),
		threads,
		deferred,
		viewerLogin,
		cl,
		2,
	)

	require.NoError(t, err)
	assert.Empty(t, results)
}

func TestPipelineForThreads_classifierErrorMarksUnclassified(t *testing.T) {
	viewerLogin := "alice"
	sentinelErr := errors.New("classifier failure")
	cl := fakeClassifier{
		err: sentinelErr,
	}

	threads := []*forge.ReviewThreadItem{
		{ID: "thread-1", Body: "comment"},
		{ID: "thread-2", Body: "another comment"},
	}

	results, err := review.PipelineForThreads(
		t.Context(),
		threads,
		nil,
		viewerLogin,
		cl,
		1,
	)

	// An error is returned, but results still come back.
	require.ErrorIs(t, err, sentinelErr)
	require.Len(t, results, 2)
	assert.Equal(t, "unclassified", results[0].Classification.Category)
	assert.Equal(t, "unclassified", results[1].Classification.Category)
}

func TestPipelineForThreads_concurrencyCap(t *testing.T) {
	viewerLogin := "alice"
	const numThreads = 20
	const concurrency = 3

	cl := &countingClassifier{
		resp: &claude.Classification{Category: "suggestion"},
	}

	threads := make([]*forge.ReviewThreadItem, numThreads)
	for i := range numThreads {
		threads[i] = &forge.ReviewThreadItem{
			ID:   forge.ReviewThreadID(string(rune('A' + i))),
			Body: "comment",
		}
	}

	results, err := review.PipelineForThreads(
		t.Context(),
		threads,
		nil,
		viewerLogin,
		cl,
		concurrency,
	)

	require.NoError(t, err)
	require.Len(t, results, numThreads)

	// Verify all items classified correctly.
	for _, r := range results {
		assert.Equal(t, "suggestion", r.Classification.Category)
	}

	// Concurrency must never have exceeded the cap.
	assert.LessOrEqual(t, cl.peak.Load(), int64(concurrency))
}

func TestPipelineForThreads_orderPreserved(t *testing.T) {
	viewerLogin := "alice"
	cl := fakeClassifier{
		resp: &claude.Classification{Category: "nit"},
	}

	threads := []*forge.ReviewThreadItem{
		{ID: "t1", Body: "first"},
		{ID: "t2", Body: "second"},
		{ID: "t3", Body: "third"},
	}

	results, err := review.PipelineForThreads(
		t.Context(),
		threads,
		nil,
		viewerLogin,
		cl,
		4, // higher concurrency to stress ordering
	)

	require.NoError(t, err)
	require.Len(t, results, 3)
	assert.Equal(t, threads[0], results[0].Item.Source)
	assert.Equal(t, threads[1], results[1].Item.Source)
	assert.Equal(t, threads[2], results[2].Item.Source)
}

func TestPipelineForThreads_concurrencyLessThanOne(t *testing.T) {
	viewerLogin := "alice"
	cl := fakeClassifier{
		resp: &claude.Classification{Category: "question"},
	}

	threads := []*forge.ReviewThreadItem{
		{ID: "t1", Body: "comment"},
	}

	// concurrency < 1 should be treated as 1.
	results, err := review.PipelineForThreads(
		t.Context(),
		threads,
		nil,
		viewerLogin,
		cl,
		0,
	)

	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "question", results[0].Classification.Category)
}
