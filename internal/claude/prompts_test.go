package claude

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildPrompt(t *testing.T) {
	t.Run("SimpleReplacement", func(t *testing.T) {
		template := "Review changes in {branch} against {base}"
		vars := map[string]string{
			"branch": "feature",
			"base":   "main",
		}

		result := BuildPrompt(template, vars)
		assert.Equal(t, "Review changes in feature against main", result)
	})

	t.Run("MultipleReplacements", func(t *testing.T) {
		template := "{branch} {branch} {base}"
		vars := map[string]string{
			"branch": "foo",
			"base":   "bar",
		}

		result := BuildPrompt(template, vars)
		assert.Equal(t, "foo foo bar", result)
	})

	t.Run("MissingVariable", func(t *testing.T) {
		template := "Review {branch} against {base}"
		vars := map[string]string{
			"branch": "feature",
		}

		result := BuildPrompt(template, vars)
		// Missing variables should remain as-is.
		assert.Equal(t, "Review feature against {base}", result)
	})

	t.Run("NoVariables", func(t *testing.T) {
		template := "Simple prompt with no variables"
		vars := map[string]string{}

		result := BuildPrompt(template, vars)
		assert.Equal(t, "Simple prompt with no variables", result)
	})

	t.Run("ComplexTemplate", func(t *testing.T) {
		template := `Generate PR title and description.
Branch: {branch}, Base: {base}
Commits: {commits}
Diff: {diff}`
		vars := map[string]string{
			"branch":  "feature-x",
			"base":    "main",
			"commits": "abc123 First commit\ndef456 Second commit",
			"diff":    "+added line\n-removed line",
		}

		result := BuildPrompt(template, vars)
		assert.Contains(t, result, "feature-x")
		assert.Contains(t, result, "main")
		assert.Contains(t, result, "abc123")
		assert.Contains(t, result, "+added line")
	})
}

func TestBuildReviewPrompt(t *testing.T) {
	cfg := DefaultConfig()

	t.Run("Basic", func(t *testing.T) {
		prompt := BuildReviewPrompt(cfg, "Fix login bug", "diff content")
		assert.Contains(t, prompt, "Fix login bug")
		assert.Contains(t, prompt, "diff content")
	})
}

func TestBuildSummaryPrompt(t *testing.T) {
	cfg := DefaultConfig()

	t.Run("Basic", func(t *testing.T) {
		prompt := BuildSummaryPrompt(cfg, "feature", "main", "commit messages", "diff")
		assert.Contains(t, prompt, "feature")
		assert.Contains(t, prompt, "main")
		assert.Contains(t, prompt, "commit messages")
		assert.Contains(t, prompt, "diff")
	})
}

func TestBuildCommitPrompt(t *testing.T) {
	cfg := DefaultConfig()

	t.Run("Basic", func(t *testing.T) {
		prompt := BuildCommitPrompt(cfg, "staged diff content")
		assert.Contains(t, prompt, "staged diff content")
	})
}

func TestBuildStackReviewPrompt(t *testing.T) {
	cfg := DefaultConfig()

	t.Run("Basic", func(t *testing.T) {
		branches := "branch1: changes\nbranch2: more changes"
		prompt := BuildStackReviewPrompt(cfg, branches)
		assert.Contains(t, prompt, "branch1")
		assert.Contains(t, prompt, "branch2")
	})
}

func TestRefinePrompt(t *testing.T) {
	t.Run("Basic", func(t *testing.T) {
		original := "Generate a commit message"
		instruction := "Make it shorter"

		result := RefinePrompt(original, instruction)
		assert.Contains(t, result, original)
		assert.Contains(t, result, instruction)
	})
}
