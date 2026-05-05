package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.abhg.dev/gs/internal/forge"
)

func TestIsAlreadyAddressed_noReplies(t *testing.T) {
	thread := &forge.ReviewThreadItem{
		ID:      "t1",
		Replies: nil,
	}
	assert.False(t, IsAlreadyAddressed(thread, "alice"))
}

func TestIsAlreadyAddressed(t *testing.T) {
	const viewer = "alice"

	now := time.Now()

	tests := []struct {
		name    string
		replies []forge.ReviewReply
		want    bool
	}{
		{
			name: "OurReplyWithSubject",
			replies: []forge.ReviewReply{
				{
					Author:    viewer,
					Body:      "Addressed in abc1234: fix the thing",
					CreatedAt: now,
				},
			},
			want: true,
		},
		{
			name: "OurReplyWithoutSubject",
			replies: []forge.ReviewReply{
				{
					Author:    viewer,
					Body:      "Addressed in abc1234",
					CreatedAt: now,
				},
			},
			want: true,
		},
		{
			name: "ReviewerRepliedAfterUs",
			replies: []forge.ReviewReply{
				{
					Author:    viewer,
					Body:      "Addressed in abc1234: fix the thing",
					CreatedAt: now,
				},
				{
					Author:    "reviewer",
					Body:      "Still not right, please fix again",
					CreatedAt: now.Add(time.Hour),
				},
			},
			want: false,
		},
		{
			name: "SomeoneElseMatchingReply",
			replies: []forge.ReviewReply{
				{
					Author:    "otheruser",
					Body:      "Addressed in abc1234: fix the thing",
					CreatedAt: now,
				},
			},
			want: false,
		},
		{
			name: "OurReplyNoMatch",
			replies: []forge.ReviewReply{
				{
					Author:    viewer,
					Body:      "I'll look into this later",
					CreatedAt: now,
				},
			},
			want: false,
		},
		{
			name: "CaseInsensitiveAuthor",
			replies: []forge.ReviewReply{
				{
					Author:    "Alice", // different case
					Body:      "Addressed in abc1234",
					CreatedAt: now,
				},
			},
			want: true,
		},
		{
			name: "MinimalSHA7Chars",
			replies: []forge.ReviewReply{
				{
					Author:    viewer,
					Body:      "Addressed in abcdef0",
					CreatedAt: now,
				},
			},
			want: true,
		},
		{
			name: "ShortSHAUnder7Chars",
			replies: []forge.ReviewReply{
				{
					Author:    viewer,
					Body:      "Addressed in abc123",
					CreatedAt: now,
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			thread := &forge.ReviewThreadItem{
				ID:      "t1",
				Replies: tt.replies,
			}
			assert.Equal(t, tt.want, IsAlreadyAddressed(thread, viewer))
		})
	}
}

func TestDeferredFile_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deferred.txt")

	ids := []forge.ReviewThreadID{"id1", "id2", "id3"}
	require.NoError(t, SaveDeferred(path, ids))

	got, err := LoadDeferred(path)
	require.NoError(t, err)
	assert.Equal(t, ids, got)
}

func TestDeferredFile_MissingIsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.txt")

	got, err := LoadDeferred(path)
	assert.NoError(t, err)
	assert.Nil(t, got)
}

func TestDeferredFile_CorruptIsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deferred.txt")

	// Write a line that is too long for bufio.Scanner's default buffer
	// (64 KiB). This causes scanner.Scan to return false due to
	// bufio.ErrTooLong, which LoadDeferred treats as empty (non-fatal).
	longLine := strings.Repeat("x", 70_000) + "\n"
	require.NoError(t, os.WriteFile(path, []byte(longLine), 0o600))

	got, err := LoadDeferred(path)
	assert.NoError(t, err)
	assert.Nil(t, got)
}

func TestDeferredFile_SkipsCommentsAndBlanks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deferred.txt")

	content := "# this is a comment\n\nid1\n# another comment\nid2\n\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))

	got, err := LoadDeferred(path)
	require.NoError(t, err)
	assert.Equal(t, []forge.ReviewThreadID{"id1", "id2"}, got)
}

func TestReconcileDeferred(t *testing.T) {
	const viewer = "alice"

	now := time.Now()

	// thread1: still open, not addressed by viewer — keep
	thread1 := &forge.ReviewThreadItem{
		ID: "t1",
		Replies: []forge.ReviewReply{
			{Author: "reviewer", Body: "Please fix this", CreatedAt: now},
		},
	}
	// thread2: still open, addressed by viewer — prune
	thread2 := &forge.ReviewThreadItem{
		ID: "t2",
		Replies: []forge.ReviewReply{
			{
				Author:    viewer,
				Body:      "Addressed in abcdef1: fixed",
				CreatedAt: now,
			},
		},
	}
	// thread3: not in fetch (resolved remotely) — prune
	// thread4: in deferred but not open (removed) — prune

	open := []*forge.ReviewThreadItem{thread1, thread2}
	deferred := []forge.ReviewThreadID{"t1", "t2", "t3", "t4"}

	got := ReconcileDeferred(deferred, open, viewer)
	assert.Equal(t, []forge.ReviewThreadID{"t1"}, got)
}

func TestReconcileDeferred_emptyDeferred(t *testing.T) {
	open := []*forge.ReviewThreadItem{
		{ID: "t1"},
	}
	got := ReconcileDeferred(nil, open, "alice")
	assert.Nil(t, got)
}

func TestReconcileDeferred_emptyOpen(t *testing.T) {
	deferred := []forge.ReviewThreadID{"t1", "t2"}
	got := ReconcileDeferred(deferred, nil, "alice")
	// All deferred IDs are gone from open — all pruned.
	assert.Nil(t, got)
}
