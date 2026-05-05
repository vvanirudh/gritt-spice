package plugin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractCodeReview(t *testing.T) {
	dir, cleanup, err := ExtractCodeReview()
	require.NoError(t, err)
	defer cleanup()

	// Verify plugin.json exists with expected content.
	pluginJSON, err := os.ReadFile(
		filepath.Join(dir, ".claude-plugin", "plugin.json"),
	)
	require.NoError(t, err)
	assert.Contains(t, string(pluginJSON), `"name": "code-review"`)

	// Verify commands/code-review.md exists and is non-empty.
	cmdFile, err := os.ReadFile(
		filepath.Join(dir, "commands", "code-review.md"),
	)
	require.NoError(t, err)
	assert.Contains(t, string(cmdFile), "code review")
}

func TestExtractCodeReview_cleanup(t *testing.T) {
	dir, cleanup, err := ExtractCodeReview()
	require.NoError(t, err)

	// Verify directory exists.
	_, err = os.Stat(dir)
	require.NoError(t, err)

	// Clean up and verify directory is gone.
	cleanup()
	_, err = os.Stat(dir)
	assert.True(t, os.IsNotExist(err))
}
