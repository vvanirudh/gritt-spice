// Package plugin provides embedded Claude CLI plugins.
package plugin

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// The all: prefix is required to include the .claude-plugin dotdir.
//
//go:embed all:code-review
var _codeReviewFS embed.FS

// ExtractCodeReview extracts the embedded code-review plugin
// to a temporary directory and returns the path to the plugin root.
//
// The caller must call the returned cleanup function
// when the plugin is no longer needed.
func ExtractCodeReview() (dir string, cleanup func(), err error) {
	tmpDir, err := os.MkdirTemp("", "gs-review-*")
	if err != nil {
		return "", nil, fmt.Errorf("create temp dir: %w", err)
	}

	cleanup = func() { os.RemoveAll(tmpDir) }

	if err := extractFS(_codeReviewFS, "code-review", tmpDir); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("extract code-review plugin: %w", err)
	}

	return tmpDir, cleanup, nil
}

// extractFS walks the embedded filesystem starting at root
// and writes all files to the destination directory.
func extractFS(fsys embed.FS, root, dst string) error {
	return fs.WalkDir(fsys, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Compute relative path from the root prefix.
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("compute relative path: %w", err)
		}
		target := filepath.Join(dst, rel)

		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		data, err := fsys.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		return os.WriteFile(target, data, 0o644)
	})
}
