package review

import (
	"fmt"
	"io"
	"slices"
	"strings"
)

// PrintSummary writes a human-readable per-file summary of items to
// out. Used by `gs branch reviews` and `gs stack reviews` to render
// open review threads without taking any action.
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

// groupByFile groups items by Item.File and returns the mapping
// plus the file names in stable alphabetical order.
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
	slices.Sort(order)
	return out, order
}

// summarize returns the body to render for an item: classifier
// Summary if present, otherwise the full reviewer body
// (whitespace-collapsed for the wrapper).
func summarize(it ClassifiedItem) string {
	if it.Classification.Summary != "" {
		return it.Classification.Summary
	}
	body := strings.TrimSpace(it.Item.Body)
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
