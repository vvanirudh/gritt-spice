package review_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.abhg.dev/gs/internal/review"
	"go.abhg.dev/gs/internal/silog"
)

// initStackRepo creates a 3-branch stack: main → base → tip.
// Returns the repo dir.
//
//	main:  shared.go (orig)
//	base:  shared.go (modified) + base-only.go
//	tip:   tip-only.go
func initStackRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		require.NoError(t, cmd.Run(), "git %v", args)
	}
	write := func(path, content string) {
		t.Helper()
		require.NoError(t,
			os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644))
	}

	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	write("shared.go", "// original\n")
	run("add", "shared.go")
	run("commit", "-q", "-m", "initial shared.go")

	run("checkout", "-q", "-b", "base")
	write("shared.go", "// modified by base\n")
	write("base-only.go", "// base only\n")
	run("add", "shared.go", "base-only.go")
	run("commit", "-q", "-m", "base modifies shared, adds base-only")

	run("checkout", "-q", "-b", "tip")
	write("tip-only.go", "// tip only\n")
	run("add", "tip-only.go")
	run("commit", "-q", "-m", "tip adds tip-only")

	return dir
}

// TestFileLastBranch_baseBranchModifiedFile verifies that when a file
// was last changed in the base branch, FileLastBranch returns "base".
func TestFileLastBranch_baseBranchModifiedFile(t *testing.T) {
	dir := initStackRepo(t)

	got, err := review.FileLastBranch(
		t.Context(),
		silog.Nop(),
		dir,
		"shared.go",
		[]string{"main", "base", "tip"},
	)
	require.NoError(t, err)
	assert.Equal(t, "base", got)
}

// TestFileLastBranch_tipBranchOnly verifies that when a file is
// only present on the tip branch, FileLastBranch returns "tip".
func TestFileLastBranch_tipBranchOnly(t *testing.T) {
	dir := initStackRepo(t)

	got, err := review.FileLastBranch(
		t.Context(),
		silog.Nop(),
		dir,
		"tip-only.go",
		[]string{"main", "base", "tip"},
	)
	require.NoError(t, err)
	assert.Equal(t, "tip", got)
}

// TestFileLastBranch_unknownFile verifies that when a file is not
// touched by any branch in the list, FileLastBranch returns "".
func TestFileLastBranch_unknownFile(t *testing.T) {
	dir := initStackRepo(t)

	got, err := review.FileLastBranch(
		t.Context(),
		silog.Nop(),
		dir,
		"nonexistent.go",
		[]string{"main", "base", "tip"},
	)
	require.NoError(t, err)
	assert.Equal(t, "", got)
}

// TestFileLastBranch_baseOnlyFile verifies that a file added only in
// the base branch is attributed to base, not tip.
func TestFileLastBranch_baseOnlyFile(t *testing.T) {
	dir := initStackRepo(t)

	got, err := review.FileLastBranch(
		t.Context(),
		silog.Nop(),
		dir,
		"base-only.go",
		[]string{"main", "base", "tip"},
	)
	require.NoError(t, err)
	assert.Equal(t, "base", got)
}

// TestPreflightRestack_noConflict verifies that when merging a base
// branch into an upper branch that does not conflict, the result is empty.
func TestPreflightRestack_noConflict(t *testing.T) {
	dir := initStackRepo(t)

	// tip only adds a new file, no conflicts expected
	conflicts, err := review.PreflightRestack(
		t.Context(),
		silog.Nop(),
		dir,
		"base",
		[]string{"tip"},
	)
	require.NoError(t, err)
	assert.Empty(t, conflicts)
}

// initConflictRepo creates a repo with a conflict scenario:
// main → base (shared.go changed) → tip (shared.go changed differently).
func initConflictRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		require.NoError(t, cmd.Run(), "git %v", args)
	}
	write := func(path, content string) {
		t.Helper()
		require.NoError(t,
			os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644))
	}

	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	// Establish a common ancestor with a multi-line file.
	write("shared.go", "line1\nline2\nline3\n")
	run("add", "shared.go")
	run("commit", "-q", "-m", "initial shared.go")

	// base changes line2 in shared.go.
	run("checkout", "-q", "-b", "base")
	write("shared.go", "line1\nbase-change\nline3\n")
	run("add", "shared.go")
	run("commit", "-q", "-m", "base changes line2")

	// tip diverges from main and also changes line2 differently.
	run("checkout", "-q", "main")
	run("checkout", "-q", "-b", "tip")
	write("shared.go", "line1\ntip-change\nline3\n")
	run("add", "shared.go")
	run("commit", "-q", "-m", "tip changes line2 differently")

	return dir
}

// TestPreflightRestack_conflictsOnUpper verifies that when merging a
// base branch into an upper branch with conflicting edits,
// the conflicting branch is returned.
func TestPreflightRestack_conflictsOnUpper(t *testing.T) {
	dir := initConflictRepo(t)

	conflicts, err := review.PreflightRestack(
		t.Context(),
		silog.Nop(),
		dir,
		"base",
		[]string{"tip"},
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"tip"}, conflicts)
}
