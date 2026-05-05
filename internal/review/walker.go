package review

import (
	"context"
	"fmt"
	"strings"

	"go.abhg.dev/gs/internal/forge"
	"go.abhg.dev/gs/internal/ui"
)

// AddressAction is the user's per-item choice.
type AddressAction int

const (
	// ActionAddress runs the fix runner and posts an "Addressed in" reply.
	ActionAddress AddressAction = iota
	// ActionReply posts the reply draft (or edited text) to the thread.
	ActionReply
	// ActionEditReply opens $EDITOR to edit the draft before posting.
	ActionEditReply
	// ActionSkip moves to the next item without any action.
	ActionSkip
	// ActionDefer records the thread ID for later processing.
	ActionDefer
)

// String returns a short label for the action.
func (a AddressAction) String() string {
	switch a {
	case ActionAddress:
		return "address"
	case ActionReply:
		return "reply"
	case ActionEditReply:
		return "edit-reply"
	case ActionSkip:
		return "skip"
	case ActionDefer:
		return "defer"
	default:
		return "unknown"
	}
}

// ReplyPoster is the subset of forge.ReviewThreadLister we need for
// posting replies.
// Decoupled from forge so tests can mock it.
type ReplyPoster interface {
	PostReviewThreadReply(
		ctx context.Context,
		id forge.ReviewThreadID,
		body string,
	) (forge.ChangeCommentID, error)
}

// FixRunner spawns a focused session to make commits for one item.
// Returns the latest commit SHA and its subject line.
type FixRunner interface {
	Run(ctx context.Context, instructions string) (latestCommitSHA, subject string, err error)
}

// Walker drives the interactive walk-through of classified items.
type Walker struct {
	Items     []ClassifiedItem
	Poster    ReplyPoster
	FixRunner FixRunner
}

// WalkSummary tallies the outcomes of a walk.
type WalkSummary struct {
	Addressed int
	Replied   int
	Skipped   int
	Deferred  int
	Errors    []string

	// DeferredIDs are the thread IDs the caller should persist to disk.
	DeferredIDs []forge.ReviewThreadID
}

// Run prompts the user for an action per item using the given view,
// then dispatches. After each item, prints a one-line confirmation
// to view so the user sees something happened.
func (w *Walker) Run(ctx context.Context, view ui.View) (WalkSummary, error) {
	var s WalkSummary
	for i, item := range w.Items {
		action, replyText, err := w.promptAction(view, i, item)
		if err != nil {
			return s, err
		}
		before := s
		w.applyAction(ctx, &s, item, action, replyText)
		printActionResult(view, item, action, &before, &s)
	}
	return s, nil
}

// printActionResult writes a one-line confirmation of what just
// happened for an item: "✓ posted reply on #abc" or
// "✗ post reply on #abc: <error>".
func printActionResult(
	view ui.View,
	item ClassifiedItem,
	action AddressAction,
	before, after *WalkSummary,
) {
	id := identify(item)
	// If the new error count grew, print the most recent error.
	if len(after.Errors) > len(before.Errors) {
		last := after.Errors[len(after.Errors)-1]
		fmt.Fprintf(view, "  ✗ %s\n", last)
		return
	}
	switch {
	case after.Addressed > before.Addressed:
		fmt.Fprintf(view, "  ✓ addressed %s (commit + reply posted)\n", id)
	case after.Replied > before.Replied:
		fmt.Fprintf(view, "  ✓ posted reply on %s\n", id)
	case after.Deferred > before.Deferred:
		fmt.Fprintf(view, "  → deferred %s\n", id)
	case after.Skipped > before.Skipped:
		fmt.Fprintf(view, "  → skipped %s\n", id)
	default:
		// No counter changed and no error — only happens for
		// no-op paths (e.g. reply on a check item). Stay quiet.
		_ = action
	}
}

// promptAction shows a single item and asks the user what to do.
// It returns the chosen action and (for reply/edit-reply) the reply text.
func (w *Walker) promptAction(
	view ui.View,
	index int,
	item ClassifiedItem,
) (AddressAction, string, error) {
	thread, _ := item.Item.Source.(*forge.ReviewThreadItem)
	isCheck := thread == nil

	title := fmt.Sprintf(
		"Item %d/%d — %s — [%s]",
		index+1, len(w.Items),
		item.Item.File,
		item.Classification.Category,
	)
	desc := fmt.Sprintf(
		"%s\n\nFix plan:\n%s\n\nReply draft:\n%s",
		item.Item.Body,
		item.Classification.FixPlan,
		item.Classification.ReplyDraft,
	)

	options := []ui.SelectOption[AddressAction]{
		{Label: "address with agent", Value: ActionAddress},
	}
	if !isCheck {
		options = append(options,
			ui.SelectOption[AddressAction]{
				Label: "reply only",
				Value: ActionReply,
			},
			ui.SelectOption[AddressAction]{
				Label: "edit reply",
				Value: ActionEditReply,
			},
		)
	}
	options = append(options,
		ui.SelectOption[AddressAction]{Label: "skip", Value: ActionSkip},
		ui.SelectOption[AddressAction]{Label: "defer", Value: ActionDefer},
	)

	var action AddressAction
	sel := ui.NewSelect[AddressAction]().
		WithValue(&action).
		WithOptions(options...).
		WithTitle(title).
		WithDescription(desc)

	if err := ui.Run(view, sel); err != nil {
		return 0, "", fmt.Errorf("prompt action for item %d: %w", index+1, err)
	}

	// For ActionReply with a non-empty draft, post it as-is.
	// For ActionReply with an EMPTY draft (typically when
	// classification failed and we're "unclassified"), promote to
	// ActionEditReply so the user can write the reply themselves —
	// posting an empty body to a review thread is never useful.
	if action == ActionReply && strings.TrimSpace(item.Classification.ReplyDraft) != "" {
		return ActionReply, item.Classification.ReplyDraft, nil
	}
	if action != ActionEditReply && action != ActionReply {
		return action, item.Classification.ReplyDraft, nil
	}

	// Edit-reply (or reply-with-empty-draft): open $EDITOR.
	body := item.Classification.ReplyDraft
	editor := ui.NewOpenEditor(ui.Editor{}).
		WithTitle("Edit reply").
		WithValue(&body)
	if err := ui.Run(view, editor); err != nil {
		return 0, "", fmt.Errorf("edit reply for item %d: %w", index+1, err)
	}
	if strings.TrimSpace(body) == "" {
		// User left the editor empty: treat as Skip rather than
		// posting nothing.
		return ActionSkip, "", nil
	}
	// After editing, treat as a Reply action.
	return ActionReply, body, nil
}

// applyAction executes the chosen action.
// Errors are collected into s.Errors rather than returned,
// so the walk continues past individual failures.
func (w *Walker) applyAction(
	ctx context.Context,
	s *WalkSummary,
	item ClassifiedItem,
	action AddressAction,
	replyText string,
) {
	thread, _ := item.Item.Source.(*forge.ReviewThreadItem)

	switch action {
	case ActionAddress:
		if w.FixRunner == nil {
			s.Errors = append(s.Errors, fmt.Sprintf(
				"item %s: fix runner not configured",
				identify(item),
			))
			return
		}
		sha, subject, err := w.FixRunner.Run(ctx, buildSingleItemInstructions(item))
		if err != nil {
			s.Errors = append(s.Errors, fmt.Sprintf(
				"item %s: fix: %v", identify(item), err,
			))
			return
		}
		if thread != nil {
			if _, err := w.Poster.PostReviewThreadReply(
				ctx, thread.ID, BuildAddressedReply(sha, subject),
			); err != nil {
				s.Errors = append(s.Errors, fmt.Sprintf(
					"item %s: post reply: %v", identify(item), err,
				))
				return
			}
		}
		s.Addressed++

	case ActionReply, ActionEditReply:
		if thread == nil {
			// Checks have no thread; reply is a no-op.
			return
		}
		if _, err := w.Poster.PostReviewThreadReply(
			ctx, thread.ID, replyText,
		); err != nil {
			s.Errors = append(s.Errors, fmt.Sprintf(
				"item %s: post reply: %v", identify(item), err,
			))
			return
		}
		s.Replied++

	case ActionSkip:
		s.Skipped++

	case ActionDefer:
		s.Deferred++
		if thread != nil {
			s.DeferredIDs = append(s.DeferredIDs, thread.ID)
		}
	}
}

// ApplyActionsForTest drives applyAction with a pre-determined sequence of
// actions and reply texts, bypassing the interactive UI.
// It is intended only for unit tests.
func (w *Walker) ApplyActionsForTest(
	ctx context.Context,
	actions []AddressAction,
	replyTexts []string,
) WalkSummary {
	var s WalkSummary
	for i, item := range w.Items {
		action := ActionSkip
		if i < len(actions) {
			action = actions[i]
		}
		replyText := ""
		if i < len(replyTexts) {
			replyText = replyTexts[i]
		}
		w.applyAction(ctx, &s, item, action, replyText)
	}
	return s
}

// identify returns a short identifier for an item, used in error messages.
func identify(item ClassifiedItem) string {
	if t, ok := item.Item.Source.(*forge.ReviewThreadItem); ok {
		return fmt.Sprintf("#%s", t.ID)
	}
	return item.Item.Title
}

// buildSingleItemInstructions writes the instructions for a single-item
// fix session. The spawned Claude reads this and commits with an
// Addresses-# marker.
//
// The instructions include the diff hunk and line range from the
// review thread so the agent doesn't have to grep/read to find the
// reviewed code — it can jump straight to the right place. Empirically
// this halves session time on most items.
func buildSingleItemInstructions(item ClassifiedItem) string {
	var b strings.Builder

	id := "unknown"
	if t, ok := item.Item.Source.(*forge.ReviewThreadItem); ok {
		id = string(t.ID)
	}

	fmt.Fprintf(&b, "# Address one review item\n\n")
	fmt.Fprintf(&b, "## %s (#%s)\n\n", item.Item.File, id)

	if item.Item.Author != "" {
		fmt.Fprintf(&b, "**Reviewer:** %s\n", item.Item.Author)
	}
	if item.Item.LineRange != [2]int{0, 0} {
		fmt.Fprintf(&b, "**Lines:** %d-%d\n",
			item.Item.LineRange[0], item.Item.LineRange[1])
	}
	if item.Classification.Category != "" {
		fmt.Fprintf(&b, "**Category:** %s\n", item.Classification.Category)
	}
	b.WriteString("\n")

	fmt.Fprintf(&b, "### Comment\n\n%s\n\n", item.Item.Body)

	if item.Item.Hunk != "" {
		fmt.Fprintf(&b, "### Reviewed code (diff hunk)\n\n```\n%s\n```\n\n",
			item.Item.Hunk)
	}

	if item.Classification.FixPlan != "" {
		fmt.Fprintf(&b, "### Suggested fix plan\n\n%s\n\n",
			item.Classification.FixPlan)
	}

	fmt.Fprintf(&b, "### Required commit message\n\n"+
		"The commit body MUST include this exact line so git-spice can "+
		"link the commit back to the review thread:\n\n"+
		"    Addresses #%s\n\n", id)

	b.WriteString(_instructionScopeFooter)

	return b.String()
}

// _instructionScopeFooter is appended to every INSTRUCTIONS.md to
// keep agent sessions tight: no full-suite tests, no unrelated
// refactors, no auto-pushes. Reduces wall time per session.
const _instructionScopeFooter = `### Scope and speed guidance

- Touch only the file(s) referenced above. Don't refactor unrelated code.
- For Go projects, use mcp__gopls__go_diagnostics if available (faster than
  go build/go vet for compile checks).
- Run only the tests for the package(s) you modified, e.g.
  ` + "`go test ./path/to/package -run TestName`" + ` — never
  ` + "`go test ./...`" + ` (slow).
- Don't add new tests unless the comment explicitly asks for one.
- Don't push. git-spice handles that after you exit.
- Make ONE commit per item with the required Addresses-# marker.
`
