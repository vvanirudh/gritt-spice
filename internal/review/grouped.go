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
			// Render with a leading "[cat] " marker on the first line
			// and a hanging indent for wrapped continuations.
			marker := fmt.Sprintf("    [%s] ", cat)
			indent := strings.Repeat(" ", len(marker))
			lines := wrapText(summarize(it), 78-len(indent))
			for i, line := range lines {
				if i == 0 {
					fmt.Fprintln(out, marker+line)
				} else {
					fmt.Fprintln(out, indent+line)
				}
			}
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
			printFileResult(view, file, len(groupItems), sub)
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
			fmt.Fprintf(view, "  → skipped all %d in %s\n",
				len(groupItems), file)
		}
	}
	return s, nil
}

// printFileResult writes a one-line per-file summary after a
// "address all together" batch session completes.
func printFileResult(
	view io.Writer,
	file string,
	totalItems int,
	sub WalkSummary,
) {
	addressed := sub.Addressed
	skipped := sub.Skipped
	errs := len(sub.Errors)
	switch {
	case errs > 0:
		fmt.Fprintf(view, "  ✗ %s: %d/%d addressed, %d error(s)\n",
			file, addressed, totalItems, errs)
	case addressed == totalItems:
		fmt.Fprintf(view, "  ✓ %s: addressed all %d\n", file, totalItems)
	default:
		fmt.Fprintf(view, "  ✓ %s: %d/%d addressed (%d skipped)\n",
			file, addressed, totalItems, skipped)
	}
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

// summarize returns a description of a classified item for the
// printed summary. Returns the classifier's Summary if present,
// otherwise the full body (whitespace-collapsed). The caller is
// responsible for any wrapping needed for terminal display.
//
// The body is intentionally NOT truncated — when the user runs the
// summary they want to see the actual reviewer feedback, not a
// preview. Use --fix if you want the agent's classification to
// produce a tighter Summary instead.
func summarize(it ClassifiedItem) string {
	if it.Classification.Summary != "" {
		return it.Classification.Summary
	}
	body := strings.TrimSpace(it.Item.Body)
	// Collapse internal whitespace + newlines to single spaces so
	// the wrapper has full text to break on word boundaries.
	return strings.Join(strings.Fields(body), " ")
}

// wrapText word-wraps s to lines of at most width characters,
// returning the wrapped lines without trailing newlines. Long words
// that exceed width are kept on their own line (not split).
func wrapText(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	cur := words[0]
	for _, w := range words[1:] {
		if len(cur)+1+len(w) > width {
			lines = append(lines, cur)
			cur = w
		} else {
			cur += " " + w
		}
	}
	lines = append(lines, cur)
	return lines
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
