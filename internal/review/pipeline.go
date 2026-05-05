// Package review provides helpers for fetching and summarizing
// PR review threads — used by `gs branch reviews`.
package review

import (
	"context"
	"regexp"
	"strings"

	"go.abhg.dev/gs/internal/forge"
)

// addressedRE permissively matches an "Addressed in <sha>" marker,
// with or without a trailing ": <subject>".
var addressedRE = regexp.MustCompile(`Addressed in [0-9a-f]{7,}\b`)

// ClassifiedItem is the unit consumed by PrintSummary. The
// "Classification" naming is a vestige of an earlier richer model;
// today it just carries category/summary fields that the summary
// printer renders verbatim. Empty values are fine.
type ClassifiedItem struct {
	Item           *Item
	Classification *Classification
}

// Item describes one review thread for display purposes.
type Item struct {
	File   string
	Author string
	Body   string
}

// Classification carries optional metadata for an item. All fields
// are optional; PrintSummary handles empty values gracefully.
type Classification struct {
	Category string
	Summary  string
}

// IsAlreadyAddressed reports whether the most recent reply on the
// thread is from viewerLogin AND matches the addressed marker.
//
// The check is "did WE address it" — if the reviewer replied after
// our addressed-in marker (asking for more changes), the thread is
// considered re-opened and this returns false.
func IsAlreadyAddressed(thread *forge.ReviewThreadItem, viewerLogin string) bool {
	if len(thread.Replies) == 0 {
		return false
	}
	latest := &thread.Replies[len(thread.Replies)-1]
	if !strings.EqualFold(latest.Author, viewerLogin) {
		return false
	}
	return addressedRE.MatchString(latest.Body)
}

// PipelineForThreads filters the given threads down to those still
// worth showing the user (drops already-addressed-by-viewer
// threads), and returns them as ClassifiedItems ready for
// PrintSummary.
func PipelineForThreads(
	_ context.Context,
	threads []*forge.ReviewThreadItem,
	viewerLogin string,
) []ClassifiedItem {
	var out []ClassifiedItem
	for _, t := range threads {
		if IsAlreadyAddressed(t, viewerLogin) {
			continue
		}
		out = append(out, ClassifiedItem{
			Item: &Item{
				File:   t.File,
				Author: t.Author,
				Body:   t.Body,
			},
			Classification: &Classification{},
		})
	}
	return out
}
