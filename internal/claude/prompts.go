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

// ParseTitleBody extracts title and body from Claude's response.
// It looks for "TITLE:" or "SUBJECT:" prefixes, with optional "BODY:" prefix.
// Falls back to using first line as title, rest as body.
func ParseTitleBody(response string) (title, body string) {
	lines := strings.Split(strings.TrimSpace(response), "\n")
	if len(lines) == 0 {
		return response, ""
	}

	// Look for TITLE: or SUBJECT: prefix.
	for i, line := range lines {
		lineLower := strings.ToLower(line)

		var prefixLen int
		switch {
		case strings.HasPrefix(lineLower, "title:"):
			prefixLen = 6
		case strings.HasPrefix(lineLower, "subject:"):
			prefixLen = 8
		default:
			continue
		}

		title = strings.TrimSpace(line[prefixLen:])
		if i+1 < len(lines) {
			// Skip empty lines and handle BODY: prefix.
			remaining := lines[i+1:]
			for j, l := range remaining {
				lLower := strings.ToLower(l)
				if strings.HasPrefix(lLower, "body:") {
					// Extract body content after "body:" prefix.
					bodyContent := strings.TrimSpace(l[5:])
					if bodyContent != "" {
						// Body content on same line as prefix.
						remaining = append([]string{bodyContent}, remaining[j+1:]...)
					} else {
						remaining = remaining[j+1:]
					}
					break
				}
				if strings.TrimSpace(l) != "" {
					remaining = remaining[j:]
					break
				}
			}
			body = strings.TrimSpace(strings.Join(remaining, "\n"))
		}
		return title, body
	}

	// Fallback: first line is title, rest is body.
	title = lines[0]
	if len(lines) > 1 {
		body = strings.TrimSpace(strings.Join(lines[1:], "\n"))
	}
	return title, body
}
