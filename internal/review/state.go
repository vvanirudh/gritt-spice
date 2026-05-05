// Package review provides the orchestrator for the pull-and-address
// workflow: filtering review threads, walking through them, and
// posting addressed replies.
package review

import (
	"bufio"
	"errors"
	"os"
	"regexp"
	"strings"

	"go.abhg.dev/gs/internal/forge"
)

// addressedRE permissively matches an "Addressed in <sha>" marker,
// with or without a trailing ": <subject>".
var addressedRE = regexp.MustCompile(`Addressed in [0-9a-f]{7,}\b`)

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

// LoadDeferred reads the deferred-thread-IDs file. Lines starting
// with `#` and blank lines are ignored. A missing file or a corrupt
// file are both treated as empty (non-fatal).
func LoadDeferred(path string) ([]forge.ReviewThreadID, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var ids []forge.ReviewThreadID
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		ids = append(ids, forge.ReviewThreadID(line))
	}
	// A scanner error is treated as "empty" rather than a hard failure
	// — the file is a hint, not a source of truth.
	return ids, nil
}

// SaveDeferred writes the deferred-thread-IDs file atomically (write
// to .tmp then rename). Existing comments are not preserved.
func SaveDeferred(path string, ids []forge.ReviewThreadID) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	_, _ = w.WriteString(
		"# Deferred review threads (cleared on remote resolution / new comment)\n",
	)
	for _, id := range ids {
		_, _ = w.WriteString(string(id) + "\n")
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReconcileDeferred returns the IDs that should remain deferred
// after comparing them against a fresh open-thread fetch:
//
//   - IDs no longer in the fetch (resolved/deleted remotely) are pruned.
//   - IDs that the viewer has now addressed are pruned.
//   - All others are kept.
func ReconcileDeferred(
	deferred []forge.ReviewThreadID,
	open []*forge.ReviewThreadItem,
	viewerLogin string,
) []forge.ReviewThreadID {
	openByID := make(map[forge.ReviewThreadID]*forge.ReviewThreadItem, len(open))
	for _, t := range open {
		openByID[t.ID] = t
	}
	var keep []forge.ReviewThreadID
	for _, id := range deferred {
		t, ok := openByID[id]
		if !ok {
			continue
		}
		if IsAlreadyAddressed(t, viewerLogin) {
			continue
		}
		keep = append(keep, id)
	}
	return keep
}
