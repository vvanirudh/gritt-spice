package claude

import (
	"strings"
)

// BuildPrompt replaces placeholders in a template with provided values.
// Placeholders are in the format {key}.
// Missing keys are left as-is.
func BuildPrompt(template string, vars map[string]string) string {
	result := template
	for key, value := range vars {
		placeholder := "{" + key + "}"
		result = strings.ReplaceAll(result, placeholder, value)
	}
	return result
}

// BuildReviewPrompt builds a code review prompt.
func BuildReviewPrompt(cfg *Config, title, diff string) string {
	return BuildPrompt(cfg.Prompts.Review, map[string]string{
		"title": title,
		"diff":  diff,
	})
}

// BuildSummaryPrompt builds a PR summary generation prompt.
func BuildSummaryPrompt(cfg *Config, branch, base, commits, diff string) string {
	return BuildPrompt(cfg.Prompts.Summary, map[string]string{
		"branch":  branch,
		"base":    base,
		"commits": commits,
		"diff":    diff,
	})
}

// BuildCommitPrompt builds a commit message generation prompt.
func BuildCommitPrompt(cfg *Config, diff string) string {
	return BuildPrompt(cfg.Prompts.Commit, map[string]string{
		"diff": diff,
	})
}

// BuildStackReviewPrompt builds a stack review prompt.
func BuildStackReviewPrompt(cfg *Config, branches string) string {
	return BuildPrompt(cfg.Prompts.StackReview, map[string]string{
		"branches": branches,
	})
}

// RefinePrompt appends a refinement instruction to an original prompt.
func RefinePrompt(original, instruction string) string {
	return original + "\n\nAdditional instruction: " + instruction
}
