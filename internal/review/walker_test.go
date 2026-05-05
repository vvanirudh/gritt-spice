package review_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.abhg.dev/gs/internal/claude"
	"go.abhg.dev/gs/internal/forge"
	"go.abhg.dev/gs/internal/review"
)

// fakeReplyPoster records PostReviewThreadReply calls.
type fakeReplyPoster struct {
	calls []string // formatted as "<id>: <body>"
	err   error
}

func (f *fakeReplyPoster) PostReviewThreadReply(
	_ context.Context,
	id forge.ReviewThreadID,
	body string,
) (forge.ChangeCommentID, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.calls = append(f.calls, string(id)+": "+body)
	return nil, nil // tests don't care about the ID
}

// fakeFixRunner returns fixed values.
type fakeFixRunner struct {
	sha     string
	subject string
	err     error
}

func (f fakeFixRunner) Run(
	_ context.Context,
	_ string,
) (string, string, error) {
	return f.sha, f.subject, f.err
}

// threadItem builds a ClassifiedItem backed by a ReviewThreadItem.
func threadItem(id, file, body, replyDraft, fixPlan string) review.ClassifiedItem {
	thread := &forge.ReviewThreadItem{
		ID:   forge.ReviewThreadID(id),
		File: file,
		Body: body,
	}
	return review.ClassifiedItem{
		Item: &claude.Item{
			Kind:   "review-thread",
			File:   file,
			Body:   body,
			Source: thread,
		},
		Classification: &claude.Classification{
			Category:   "nit",
			ReplyDraft: replyDraft,
			FixPlan:    fixPlan,
		},
	}
}

// checkItem builds a ClassifiedItem with no Source (simulating a check).
func checkItem(title, file, body, fixPlan string) review.ClassifiedItem {
	return review.ClassifiedItem{
		Item: &claude.Item{
			Kind:  "check",
			Title: title,
			File:  file,
			Body:  body,
			// Source intentionally nil — no ReviewThreadItem
		},
		Classification: &claude.Classification{
			Category: "bug",
			FixPlan:  fixPlan,
		},
	}
}

// TestWalker_address_postsReply verifies that ActionAddress runs the fix
// runner and posts an "Addressed in" reply to the thread.
func TestWalker_address_postsReply(t *testing.T) {
	poster := &fakeReplyPoster{}
	runner := fakeFixRunner{sha: "abc1234def", subject: "fix nit in foo.go"}

	item := threadItem("thread-1", "foo.go", "rename this", "draft reply", "rename x to y")
	w := &review.Walker{
		Items:     []review.ClassifiedItem{item},
		Poster:    poster,
		FixRunner: runner,
	}

	summary := w.ApplyActionsForTest(
		t.Context(),
		[]review.AddressAction{review.ActionAddress},
		[]string{""},
	)

	assert.Equal(t, 1, summary.Addressed)
	assert.Equal(t, 0, summary.Replied)
	assert.Empty(t, summary.Errors)
	require.Len(t, poster.calls, 1)
	assert.Contains(t, poster.calls[0], "thread-1:")
	assert.Contains(t, poster.calls[0], "Addressed in abc1234")
	assert.Contains(t, poster.calls[0], "fix nit in foo.go")
}

// TestWalker_replyOnly_postsReply verifies that ActionReply posts the
// provided reply text without running the fix runner.
func TestWalker_replyOnly_postsReply(t *testing.T) {
	poster := &fakeReplyPoster{}
	item := threadItem("thread-2", "bar.go", "nice comment", "looks good!", "")
	w := &review.Walker{
		Items:  []review.ClassifiedItem{item},
		Poster: poster,
	}

	summary := w.ApplyActionsForTest(
		t.Context(),
		[]review.AddressAction{review.ActionReply},
		[]string{"looks good!"},
	)

	assert.Equal(t, 0, summary.Addressed)
	assert.Equal(t, 1, summary.Replied)
	assert.Empty(t, summary.Errors)
	require.Len(t, poster.calls, 1)
	assert.Equal(t, "thread-2: looks good!", poster.calls[0])
}

// TestWalker_skip_noOp verifies that ActionSkip increments the skip
// counter and does not post any replies.
func TestWalker_skip_noOp(t *testing.T) {
	poster := &fakeReplyPoster{}
	item := threadItem("thread-3", "baz.go", "minor", "ok", "")
	w := &review.Walker{
		Items:  []review.ClassifiedItem{item},
		Poster: poster,
	}

	summary := w.ApplyActionsForTest(
		t.Context(),
		[]review.AddressAction{review.ActionSkip},
		[]string{""},
	)

	assert.Equal(t, 0, summary.Addressed)
	assert.Equal(t, 0, summary.Replied)
	assert.Equal(t, 1, summary.Skipped)
	assert.Empty(t, poster.calls)
	assert.Empty(t, summary.Errors)
}

// TestWalker_defer_recordsID verifies that ActionDefer records the thread
// ID in DeferredIDs.
func TestWalker_defer_recordsID(t *testing.T) {
	item := threadItem("thread-4", "qux.go", "later", "will do later", "")
	w := &review.Walker{
		Items:  []review.ClassifiedItem{item},
		Poster: &fakeReplyPoster{},
	}

	summary := w.ApplyActionsForTest(
		t.Context(),
		[]review.AddressAction{review.ActionDefer},
		[]string{""},
	)

	assert.Equal(t, 1, summary.Deferred)
	assert.Equal(t, []forge.ReviewThreadID{"thread-4"}, summary.DeferredIDs)
	assert.Empty(t, summary.Errors)
}

// TestWalker_address_fixRunnerError_collectedNotFatal verifies that a fix
// runner error is collected into Errors and the walk continues.
func TestWalker_address_fixRunnerError_collectedNotFatal(t *testing.T) {
	poster := &fakeReplyPoster{}
	runner := fakeFixRunner{err: errors.New("claude timed out")}

	item1 := threadItem("t1", "a.go", "fix me", "draft", "plan")
	item2 := threadItem("t2", "b.go", "fix me too", "draft2", "plan2")
	w := &review.Walker{
		Items:     []review.ClassifiedItem{item1, item2},
		Poster:    poster,
		FixRunner: runner,
	}

	summary := w.ApplyActionsForTest(
		t.Context(),
		[]review.AddressAction{review.ActionAddress, review.ActionSkip},
		[]string{"", ""},
	)

	// First item fails; second is skipped.
	assert.Equal(t, 0, summary.Addressed)
	assert.Equal(t, 1, summary.Skipped)
	require.Len(t, summary.Errors, 1)
	assert.Contains(t, summary.Errors[0], "claude timed out")
	assert.Empty(t, poster.calls)
}

// TestWalker_address_postReplyError_collectedNotFatal verifies that a
// reply posting error is collected and does not abort the walk.
func TestWalker_address_postReplyError_collectedNotFatal(t *testing.T) {
	poster := &fakeReplyPoster{err: errors.New("network error")}
	runner := fakeFixRunner{sha: "deadbeef", subject: "fix it"}

	item1 := threadItem("t1", "a.go", "fix me", "draft", "plan")
	item2 := threadItem("t2", "b.go", "fix me too", "draft2", "plan2")
	w := &review.Walker{
		Items:     []review.ClassifiedItem{item1, item2},
		Poster:    poster,
		FixRunner: runner,
	}

	summary := w.ApplyActionsForTest(
		t.Context(),
		[]review.AddressAction{review.ActionAddress, review.ActionReply},
		[]string{"", "draft2"},
	)

	// Both items tried; both failed to post reply.
	assert.Equal(t, 0, summary.Addressed)
	assert.Equal(t, 0, summary.Replied)
	require.Len(t, summary.Errors, 2)
	assert.Contains(t, summary.Errors[0], "network error")
	assert.Contains(t, summary.Errors[1], "network error")
}

// TestWalker_check_addressNoReply verifies that addressing a check item
// (nil Source) runs the fix runner but does not attempt to post a reply.
func TestWalker_check_addressNoReply(t *testing.T) {
	poster := &fakeReplyPoster{}
	runner := fakeFixRunner{sha: "cafebabe", subject: "fix lint"}

	item := checkItem("lint-check", "main.go", "lint failure log", "fix the lint")
	w := &review.Walker{
		Items:     []review.ClassifiedItem{item},
		Poster:    poster,
		FixRunner: runner,
	}

	summary := w.ApplyActionsForTest(
		t.Context(),
		[]review.AddressAction{review.ActionAddress},
		[]string{""},
	)

	assert.Equal(t, 1, summary.Addressed)
	assert.Empty(t, poster.calls)
	assert.Empty(t, summary.Errors)
}
