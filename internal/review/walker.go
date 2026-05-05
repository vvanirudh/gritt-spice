package review

import (
	"context"
	"fmt"

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
// then dispatches.
func (w *Walker) Run(ctx context.Context, view ui.View) (WalkSummary, error) {
	var s WalkSummary
	for i, item := range w.Items {
		action, replyText, err := w.promptAction(view, i, item)
		if err != nil {
			return s, err
		}
		w.applyAction(ctx, &s, item, action, replyText)
	}
	return s, nil
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

	if action != ActionEditReply {
		return action, item.Classification.ReplyDraft, nil
	}

	// Edit-reply: open $EDITOR with the draft.
	body := item.Classification.ReplyDraft
	editor := ui.NewOpenEditor(ui.Editor{}).
		WithTitle("Edit reply").
		WithValue(&body)
	if err := ui.Run(view, editor); err != nil {
		return 0, "", fmt.Errorf("edit reply for item %d: %w", index+1, err)
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
func buildSingleItemInstructions(item ClassifiedItem) string {
	id := "unknown"
	if t, ok := item.Item.Source.(*forge.ReviewThreadItem); ok {
		id = string(t.ID)
	}
	return fmt.Sprintf(
		"# Address one review item\n\n"+
			"## %s (#%s)\n\n"+
			"Body:\n%s\n\n"+
			"Fix plan:\n%s\n\n"+
			"Commit message must include the line: Addresses #%s\n",
		item.Item.File, id,
		item.Item.Body,
		item.Classification.FixPlan,
		id,
	)
}
