package review

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"go.abhg.dev/gs/internal/ui"
)

// GroupAction is the per-file group choice.
type GroupAction int

const (
	// GroupActionAddressAll spawns one Claude session for all items
	// in this file (batch-style, scoped to one file).
	GroupActionAddressAll GroupAction = iota

	// GroupActionWalk falls through to the per-item walker for the
	// items in this file (today's existing behavior).
	GroupActionWalk

	// GroupActionSkip skips every item in this file.
	GroupActionSkip
)

// PrintSummary writes a human-readable summary of all items grouped
// by file to out. Used at the start of an interactive session so the
// user sees the landscape before being prompted per file.
func PrintSummary(out io.Writer, items []ClassifiedItem) {
	if len(items) == 0 {
		return
	}
	groups, fileOrder := groupByFile(items)

	fmt.Fprintf(out, "\n%d review thread(s) across %d file(s):\n\n",
		len(items), len(groups))
	for _, file := range fileOrder {
		group := groups[file]
		header := file
		if header == "" {
			header = "(no file)"
		}
		fmt.Fprintf(out, "  %s (%d):\n", header, len(group))
		for _, it := range group {
			cat := it.Classification.Category
			if cat == "" {
				cat = "unclassified"
			}
			line := summarize(it)
			fmt.Fprintf(out, "    [%s] %s\n", cat, line)
		}
	}
	fmt.Fprintln(out)
}

// RunGrouped walks per-file groups instead of per-item. For each
// file, the user picks one of:
//
//   - address all (batch this file's items into one Claude session)
//   - walk individually (fall through to per-item walker)
//   - skip all (mark every item in this file as skipped)
//
// batchRun is the per-file batch runner; subjectLookup is used to
// build "Addressed in <sha>: <subject>" replies after the session
// commits.
//
// Falls back to the same behavior as Walker.Run for the per-item
// path when the user picks GroupActionWalk.
func (w *Walker) RunGrouped(
	ctx context.Context,
	view ui.View,
	batchRun BatchRunner,
	subjectLookup func(sha string) (string, error),
	instructionsPath string,
) (WalkSummary, error) {
	groups, fileOrder := groupByFile(w.Items)

	var s WalkSummary
	for i, file := range fileOrder {
		groupItems := groups[file]
		action, err := promptGroupAction(view, i+1, len(fileOrder), file, groupItems)
		if err != nil {
			return s, err
		}

		switch action {
		case GroupActionAddressAll:
			sub, err := RunBatch(
				ctx, groupItems, instructionsPath,
				batchRun, w.Poster, subjectLookup,
			)
			if err != nil {
				s.Errors = append(s.Errors,
					fmt.Sprintf("file %q: batch: %v", file, err))
				continue
			}
			s.merge(sub)

		case GroupActionWalk:
			subWalker := &Walker{
				Items:     groupItems,
				Poster:    w.Poster,
				FixRunner: w.FixRunner,
			}
			sub, err := subWalker.Run(ctx, view)
			if err != nil {
				s.Errors = append(s.Errors,
					fmt.Sprintf("file %q: walk: %v", file, err))
				continue
			}
			s.merge(sub)

		case GroupActionSkip:
			s.Skipped += len(groupItems)
		}
	}
	return s, nil
}

// merge folds rhs into s. Used to accumulate per-group summaries.
func (s *WalkSummary) merge(rhs WalkSummary) {
	s.Addressed += rhs.Addressed
	s.Replied += rhs.Replied
	s.Skipped += rhs.Skipped
	s.Deferred += rhs.Deferred
	s.Errors = append(s.Errors, rhs.Errors...)
	s.DeferredIDs = append(s.DeferredIDs, rhs.DeferredIDs...)
}

// groupByFile groups items by Item.File and returns the mapping
// plus the file names in stable insertion order.
func groupByFile(items []ClassifiedItem) (map[string][]ClassifiedItem, []string) {
	out := make(map[string][]ClassifiedItem, len(items))
	var order []string
	for _, it := range items {
		key := it.Item.File
		if _, seen := out[key]; !seen {
			order = append(order, key)
		}
		out[key] = append(out[key], it)
	}
	// Stable, deterministic ordering: alphabetical by file.
	slices.Sort(order)
	return out, order
}

// summarize returns a one-line description of a classified item
// for the printed summary table.
func summarize(it ClassifiedItem) string {
	if it.Classification.Summary != "" {
		return it.Classification.Summary
	}
	// Fall back to first line of the body.
	body := it.Item.Body
	for i, r := range body {
		if r == '\n' {
			body = body[:i]
			break
		}
	}
	if len(body) > 100 {
		body = body[:97] + "..."
	}
	return body
}

// promptGroupAction asks the user how to handle all items in a single
// file: address them all together, walk one-by-one, or skip the whole
// file.
func promptGroupAction(
	view ui.View,
	index, total int,
	file string,
	items []ClassifiedItem,
) (GroupAction, error) {
	header := file
	if header == "" {
		header = "(no file)"
	}
	title := fmt.Sprintf(
		"File %d/%d — %s — %d comment(s)",
		index, total, header, len(items),
	)

	// Build a description that lists each comment briefly so the user
	// can decide without opening the PR in a browser.
	var descBuf strings.Builder
	for _, it := range items {
		cat := it.Classification.Category
		if cat == "" {
			cat = "unclassified"
		}
		fmt.Fprintf(&descBuf, "  [%s] %s\n", cat, summarize(it))
	}
	desc := descBuf.String()

	options := []ui.SelectOption[GroupAction]{
		{
			Label: "address all together (one Claude session)",
			Value: GroupActionAddressAll,
		},
		{
			Label: "walk individually (per-item)",
			Value: GroupActionWalk,
		},
		{
			Label: "skip all in this file",
			Value: GroupActionSkip,
		},
	}

	var action GroupAction
	sel := ui.NewSelect[GroupAction]().
		WithValue(&action).
		WithOptions(options...).
		WithTitle(title).
		WithDescription(desc)

	if err := ui.Run(view, sel); err != nil {
		return 0, fmt.Errorf("prompt group action for %s: %w", header, err)
	}
	return action, nil
}
