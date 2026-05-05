package review

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"go.abhg.dev/gs/internal/forge"
)

// BuildBatchInstructions consolidates a set of items into one
// INSTRUCTIONS.md grouped by file. The spawned Claude session is
// instructed by the embedded plugin's CLAUDE.md to make one commit
// per item with the marker `Addresses #<id>` in the body.
func BuildBatchInstructions(items []ClassifiedItem) string {
	byFile := map[string][]ClassifiedItem{}
	for _, it := range items {
		byFile[it.Item.File] = append(byFile[it.Item.File], it)
	}
	files := make([]string, 0, len(byFile))
	for f := range byFile {
		files = append(files, f)
	}
	sort.Strings(files)

	var b strings.Builder
	b.WriteString("# Address review items in batch\n\n")
	b.WriteString(
		"Make ONE commit per item. " +
			"Each commit body MUST include the line " +
			"`Addresses #<id>` so git-spice can link the commit back.\n\n",
	)
	for _, f := range files {
		fmt.Fprintf(&b, "## %s\n\n", f)
		for _, it := range byFile[f] {
			id := strings.TrimPrefix(identify(it), "#")
			fmt.Fprintf(&b,
				"### #%s (%s)\n\n"+
					"Reviewer says:\n> %s\n\n"+
					"Fix plan:\n%s\n\n"+
					"Reply (informational; gs posts it after commit): %s\n\n"+
					"Marker: `Addresses #%s`\n\n",
				id, it.Classification.Category,
				it.Item.Body,
				it.Classification.FixPlan,
				it.Classification.ReplyDraft,
				id,
			)
		}
	}
	return b.String()
}

// BatchRunner runs the spawned Claude session and returns the per-item
// commit map. Decoupled from claude.FixSession so tests can inject.
type BatchRunner interface {
	Run(
		ctx context.Context,
		instructionsPath string,
	) (perItem map[string][]string, newCommits []string, err error)
}

// RunBatch writes INSTRUCTIONS.md, spawns the batch fix session, then
// posts replies for every item the agent committed for. Items the
// agent declined (no commit with their id) are recorded as Skipped.
//
// subjectLookup may be nil; if provided it is called with the last
// commit SHA to populate the "Addressed in <sha>: <subject>" reply.
func RunBatch(
	ctx context.Context,
	items []ClassifiedItem,
	instructionsPath string,
	runner BatchRunner,
	poster ReplyPoster,
	subjectLookup func(sha string) (string, error),
) (WalkSummary, error) {
	var s WalkSummary

	if err := os.WriteFile(
		instructionsPath,
		[]byte(BuildBatchInstructions(items)),
		0o644,
	); err != nil {
		return s, fmt.Errorf("write instructions: %w", err)
	}

	perItem, _, runErr := runner.Run(ctx, instructionsPath)
	if runErr != nil {
		s.Errors = append(s.Errors,
			fmt.Sprintf("fix session: %v", runErr),
		)
	}

	for _, it := range items {
		thread, _ := it.Item.Source.(*forge.ReviewThreadItem)
		if thread == nil {
			// Check items have no thread to reply to.
			continue
		}
		shas := perItem[string(thread.ID)]
		if len(shas) == 0 {
			s.Skipped++
			continue
		}
		// Use the last SHA — the most recent commit that addressed
		// this item.
		sha := shas[len(shas)-1]
		subject := ""
		if subjectLookup != nil {
			if subj, err := subjectLookup(sha); err == nil {
				subject = subj
			}
		}
		if _, err := poster.PostReviewThreadReply(
			ctx, thread.ID, BuildAddressedReply(sha, subject),
		); err != nil {
			s.Errors = append(s.Errors, fmt.Sprintf(
				"post reply for #%s: %v", thread.ID, err,
			))
			continue
		}
		s.Addressed++
	}
	return s, nil
}
