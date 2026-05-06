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
func PrintSummary(out io.Writer, items []*Item) {
	if len(items) == 0 {
		return
	}
	groups, fileOrder := groupByFile(items)

	fmt.Fprintf(out, "\n%d review thread(s) across %d file(s):\n\n",
		len(items), len(groups))
	const indent = "    "
	for _, file := range fileOrder {
		group := groups[file]
		header := file
		if header == "" {
			header = "(no file)"
		}
		fmt.Fprintf(out, "  %s (%d):\n", header, len(group))
		for _, it := range group {
			lines := wrapText(summarize(it), 78-len(indent))
			for _, line := range lines {
				fmt.Fprintln(out, indent+line)
			}
		}
	}
	fmt.Fprintln(out)
}

// groupByFile groups items by File and returns the mapping plus the
// file names in stable alphabetical order.
func groupByFile(items []*Item) (map[string][]*Item, []string) {
	out := make(map[string][]*Item, len(items))
	var order []string
	for _, it := range items {
		if _, seen := out[it.File]; !seen {
			order = append(order, it.File)
		}
		out[it.File] = append(out[it.File], it)
	}
	slices.Sort(order)
	return out, order
}

// summarize returns the body to render for an item: the reviewer
// body with whitespace collapsed for the wrapper.
func summarize(it *Item) string {
	return strings.Join(strings.Fields(it.Body), " ")
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
