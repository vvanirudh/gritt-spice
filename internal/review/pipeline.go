package review

import (
	"context"
	"sync"

	"go.abhg.dev/gs/internal/claude"
	"go.abhg.dev/gs/internal/forge"
)

// ClassifiedItem pairs a classifiable Item with its Classification.
type ClassifiedItem struct {
	Item           *claude.Item
	Classification *claude.Classification
}

// Classifier is the interface PipelineForThreads needs.
// Production implementation wraps claude.ClassifyItem.
type Classifier interface {
	Classify(ctx context.Context, item *claude.Item) (*claude.Classification, error)
}

// PipelineForThreads filters threads (skipping deferred and
// already-addressed) then classifies the remainder with bounded
// concurrency. Order is preserved.
//
// If the classifier returns an error for an item, that item is still
// included in the result with Category: "unclassified" and the first
// such error is returned alongside the results (so callers can
// surface a warning without losing partial work).
//
// concurrency < 1 is treated as 1.
func PipelineForThreads(
	ctx context.Context,
	threads []*forge.ReviewThreadItem,
	deferred []forge.ReviewThreadID,
	viewerLogin string,
	classifier Classifier,
	concurrency int,
) ([]ClassifiedItem, error) {
	// Build deferred set for O(1) lookup.
	deferredSet := make(map[forge.ReviewThreadID]bool, len(deferred))
	for _, id := range deferred {
		deferredSet[id] = true
	}

	// Filter out deferred and already-addressed threads.
	var todo []*forge.ReviewThreadItem
	for _, t := range threads {
		if deferredSet[t.ID] {
			continue
		}
		if IsAlreadyAddressed(t, viewerLogin) {
			continue
		}
		todo = append(todo, t)
	}

	if len(todo) == 0 {
		return nil, nil
	}

	if concurrency < 1 {
		concurrency = 1
	}

	// Classify remaining threads with bounded concurrency.
	// Pre-allocate results slice to preserve order.
	results := make([]ClassifiedItem, len(todo))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex

	for i, t := range todo {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, t *forge.ReviewThreadItem) {
			defer wg.Done()
			defer func() { <-sem }()

			item := &claude.Item{
				Kind:      "review-thread",
				Body:      t.Body,
				File:      t.File,
				LineRange: t.LineRange,
				Hunk:      t.Hunk,
				Author:    t.Author,
				Source:    t,
			}
			c, err := classifier.Classify(ctx, item)
			if err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
				c = &claude.Classification{Category: "unclassified"}
			}
			results[i] = ClassifiedItem{Item: item, Classification: c}
		}(i, t)
	}
	wg.Wait()
	return results, firstErr
}
