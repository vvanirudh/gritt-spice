package claude_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.abhg.dev/gs/internal/claude"
)

func TestFixSession_parsesMarkers(t *testing.T) {
	repo := initRepo(t)
	scriptDir := t.TempDir()
	instructions := writeInstructions(t, scriptDir, "irrelevant")

	// Fake claude: makes 3 commits in the repo dir (which xec sets as
	// the subprocess cwd via WithDir(RepoRoot)).
	// First mentions #abc, second has no marker, third mentions #def.
	script := writeScript(t, scriptDir, "claude", `
git commit --allow-empty -q -m "fix abc

Addresses #abc"
git commit --allow-empty -q -m "unrelated change"
git commit --allow-empty -q -m "fix def

Resolves #def"
exit 0
`)

	res, err := (&claude.FixSession{
		PluginDir:    scriptDir, // any non-empty dir; the fake doesn't read it
		Instructions: instructions,
		RepoRoot:     repo,
		ClaudeBinary: script,
		Stdout:       &bytes.Buffer{},
		Stderr:       &bytes.Buffer{},
	}).Run(t.Context())

	require.NoError(t, err)
	assert.Len(t, res.NewCommits, 3)
	assert.Len(t, res.PerItem["abc"], 1)
	assert.Len(t, res.PerItem["def"], 1)
	assert.Len(t, res.Unmatched, 1)
	assert.False(t, res.Aborted)
}

func TestFixSession_multipleMarkersPerCommit(t *testing.T) {
	repo := initRepo(t)
	scriptDir := t.TempDir()
	instructions := writeInstructions(t, scriptDir, "irrelevant")

	// Fake claude: one commit that mentions both #abc and #def.
	script := writeScript(t, scriptDir, "claude", `
git commit --allow-empty -q -m "fix two things

Addresses #abc, also fixes #def"
exit 0
`)

	res, err := (&claude.FixSession{
		PluginDir:    scriptDir,
		Instructions: instructions,
		RepoRoot:     repo,
		ClaudeBinary: script,
		Stdout:       &bytes.Buffer{},
		Stderr:       &bytes.Buffer{},
	}).Run(t.Context())

	require.NoError(t, err)
	assert.Len(t, res.NewCommits, 1)
	sha := res.NewCommits[0]
	assert.Equal(t, []string{sha}, res.PerItem["abc"])
	assert.Equal(t, []string{sha}, res.PerItem["def"])
	assert.Empty(t, res.Unmatched)
	assert.False(t, res.Aborted)
}

func TestFixSession_aborted(t *testing.T) {
	repo := initRepo(t)
	scriptDir := t.TempDir()
	instructions := writeInstructions(t, scriptDir, "irrelevant")

	// Fake claude: makes one commit then exits non-zero.
	script := writeScript(t, scriptDir, "claude", `
git commit --allow-empty -q -m "partial fix

Addresses #abc"
exit 1
`)

	res, err := (&claude.FixSession{
		PluginDir:    scriptDir,
		Instructions: instructions,
		RepoRoot:     repo,
		ClaudeBinary: script,
		Stdout:       &bytes.Buffer{},
		Stderr:       &bytes.Buffer{},
	}).Run(t.Context())

	require.Error(t, err)
	require.NotNil(t, res, "FixResult must be returned even when aborted")
	assert.True(t, res.Aborted)
	// Commits made before abort are still captured.
	assert.Len(t, res.NewCommits, 1)
	assert.Len(t, res.PerItem["abc"], 1)
}

// writeScript writes an executable shell script and returns its path.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755))
	return path
}

// writeInstructions writes an INSTRUCTIONS.md and returns its path.
func writeInstructions(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "INSTRUCTIONS.md")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// initRepo creates a fresh git repo with one initial commit.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"commit", "--allow-empty", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		require.NoError(t, cmd.Run())
	}
	return dir
}
