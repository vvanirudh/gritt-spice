// Package claude provides integration with Claude AI for code review
// and commit message generation.
package claude

import (
	"path/filepath"
	"regexp"
	"strings"
)

// DiffFile represents a single file's diff content.
type DiffFile struct {
	// Path is the file path relative to the repository root.
	Path string

	// Content is the raw diff content for this file.
	Content string

	// Binary indicates whether this is a binary file.
	Binary bool
}

// BudgetResult contains the result of a budget check.
type BudgetResult struct {
	// OverBudget is true if the diff exceeds the line budget.
	OverBudget bool

	// TotalLines is the total number of lines in the diff.
	TotalLines int

	// MaxLines is the configured maximum line budget.
	MaxLines int

	// FileLines maps file paths to their line counts.
	FileLines map[string]int
}

var (
	// diffHeaderRegex matches the start of a new file diff.
	diffHeaderRegex = regexp.MustCompile(`^diff --git (?:"?a/(.+?)"? "?b/(.+?)"?|a/(.+?) b/(.+?))$`)

	// binaryFileRegex matches binary file markers.
	binaryFileRegex = regexp.MustCompile(`^Binary files .+ and .+ differ$`)

	// filePathRegex matches +++ lines to extract file paths.
	filePathRegex = regexp.MustCompile(`^\+\+\+ (?:"?b/(.+?)"?|/dev/null)$`)
)

// ParseDiff parses a unified diff into per-file sections.
func ParseDiff(diff string) ([]DiffFile, error) {
	var files []DiffFile
	lines := strings.Split(diff, "\n")

	var currentFile *DiffFile
	var contentBuilder strings.Builder
	// Pre-allocate capacity: estimate average file size as 1/10 of total.
	contentBuilder.Grow(len(diff) / 10)

	for i, line := range lines {
		// Check for diff header (start of new file).
		if matches := diffHeaderRegex.FindStringSubmatch(line); matches != nil {
			// Save previous file if exists.
			if currentFile != nil {
				currentFile.Content = contentBuilder.String()
				files = append(files, *currentFile)
			}

			// Extract path from diff header.
			// matches[1] and matches[2] are for quoted paths.
			// matches[3] and matches[4] are for unquoted paths.
			var destPath string
			if matches[2] != "" {
				destPath = matches[2]
			} else if matches[4] != "" {
				destPath = matches[4]
			}

			currentFile = &DiffFile{
				Path: destPath,
			}
			contentBuilder.Reset()
			contentBuilder.WriteString(line)
			contentBuilder.WriteByte('\n')
			continue
		}

		// Check for binary file marker.
		if currentFile != nil && binaryFileRegex.MatchString(line) {
			currentFile.Binary = true
			contentBuilder.WriteString(line)
			contentBuilder.WriteByte('\n')
			continue
		}

		// Check for +++ line to get definitive file path.
		if currentFile != nil {
			if matches := filePathRegex.FindStringSubmatch(line); matches != nil {
				if matches[1] != "" {
					currentFile.Path = matches[1]
				}
			}
		}

		// Add line to current file's content.
		if currentFile != nil {
			contentBuilder.WriteString(line)
			if i < len(lines)-1 {
				contentBuilder.WriteByte('\n')
			}
		}
	}

	// Save last file.
	if currentFile != nil {
		currentFile.Content = contentBuilder.String()
		files = append(files, *currentFile)
	}

	return files, nil
}

// FilterDiff filters diff files based on ignore patterns and binary status.
// Binary files are always excluded.
func FilterDiff(files []DiffFile, ignorePatterns []string) []DiffFile {
	var result []DiffFile

	for _, f := range files {
		// Exclude binary files.
		if f.Binary {
			continue
		}

		// Check against ignore patterns.
		if matchesAnyPattern(f.Path, ignorePatterns) {
			continue
		}

		result = append(result, f)
	}

	return result
}

// matchesAnyPattern checks if a path matches any of the given glob patterns.
func matchesAnyPattern(path string, patterns []string) bool {
	for _, pattern := range patterns {
		// Try matching the full path.
		matched, err := filepath.Match(pattern, path)
		if err == nil && matched {
			return true
		}

		// Try matching just the filename.
		matched, err = filepath.Match(pattern, filepath.Base(path))
		if err == nil && matched {
			return true
		}

		// Handle directory patterns like "vendor/*".
		if prefix, ok := strings.CutSuffix(pattern, "/*"); ok {
			if strings.HasPrefix(path, prefix+"/") {
				return true
			}
		}
	}
	return false
}

// CheckBudget checks if the diff is within the line budget.
func CheckBudget(files []DiffFile, maxLines int) BudgetResult {
	result := BudgetResult{
		MaxLines:  maxLines,
		FileLines: make(map[string]int),
	}

	for _, f := range files {
		lineCount := countLines(f.Content)
		result.FileLines[f.Path] = lineCount
		result.TotalLines += lineCount
	}

	result.OverBudget = result.TotalLines > maxLines
	return result
}

// countLines counts the number of lines in a string.
func countLines(s string) int {
	if s == "" {
		return 0
	}

	count := strings.Count(s, "\n")

	// If the string doesn't end with a newline, add 1 for the last line.
	if !strings.HasSuffix(s, "\n") {
		count++
	}

	return count
}

// ReconstructDiff reconstructs a diff from filtered file sections.
func ReconstructDiff(files []DiffFile) string {
	// Pre-allocate capacity to avoid reallocation.
	totalLen := 0
	for _, f := range files {
		totalLen += len(f.Content) + 1
	}

	var builder strings.Builder
	builder.Grow(totalLen)

	for i, f := range files {
		if i > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(f.Content)
	}

	return builder.String()
}
