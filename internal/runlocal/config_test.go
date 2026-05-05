package runlocal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_FromYAML(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".gitspice"), 0o755))

	yaml := `checks:
  - name: lint
    cmd: golangci-lint run
    fail_fast: true
  - name: test
    cmd: go test ./...
`
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".gitspice", "precommit.yaml"),
		[]byte(yaml),
		0o644,
	))

	checks, err := Load(dir)
	require.NoError(t, err)
	require.Len(t, checks, 2)

	assert.Equal(t, "lint", checks[0].Name)
	assert.Equal(t, "golangci-lint run", checks[0].Cmd)
	assert.True(t, checks[0].FailFast)

	assert.Equal(t, "test", checks[1].Name)
	assert.Equal(t, "go test ./...", checks[1].Cmd)
	assert.False(t, checks[1].FailFast)
}

func TestLoad_MiseAutodetect(t *testing.T) {
	dir := t.TempDir()

	miseToml := `[tasks.lint]
run = "golangci-lint run"

[tasks.test]
run = "go test ./..."
`
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "mise.toml"),
		[]byte(miseToml),
		0o644,
	))

	checks, err := Load(dir)
	require.NoError(t, err)
	require.Len(t, checks, 2)

	assert.Equal(t, "lint", checks[0].Name)
	assert.True(t, strings.HasPrefix(checks[0].Cmd, "mise run"),
		"expected cmd to start with 'mise run', got %q", checks[0].Cmd)

	assert.Equal(t, "test", checks[1].Name)
	assert.True(t, strings.HasPrefix(checks[1].Cmd, "mise run"),
		"expected cmd to start with 'mise run', got %q", checks[1].Cmd)
}

func TestLoad_Fallback(t *testing.T) {
	checks, err := Load(t.TempDir())
	require.NoError(t, err)
	require.NotEmpty(t, checks)

	assert.Equal(t, "lint", checks[0].Name)
	assert.Contains(t, checks[0].Cmd, "mise")
}

func TestLoad_YAMLTakesPrecedenceOverMise(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".gitspice"), 0o755))

	yaml := `checks:
  - name: yaml-only
    cmd: echo yaml
`
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".gitspice", "precommit.yaml"),
		[]byte(yaml),
		0o644,
	))

	miseToml := `[tasks.lint]
run = "golangci-lint run"
`
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "mise.toml"),
		[]byte(miseToml),
		0o644,
	))

	checks, err := Load(dir)
	require.NoError(t, err)
	require.Len(t, checks, 1)
	assert.Equal(t, "yaml-only", checks[0].Name)
}

func TestLoad_TimeoutParse(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".gitspice"), 0o755))

	yaml := `checks:
  - name: test
    cmd: go test ./...
    timeout: 30s
`
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".gitspice", "precommit.yaml"),
		[]byte(yaml),
		0o644,
	))

	checks, err := Load(dir)
	require.NoError(t, err)
	require.Len(t, checks, 1)
	assert.Equal(t, 30*time.Second, checks[0].Timeout)
}

func TestLoad_PreCommitFramework(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".pre-commit-config.yaml"),
		[]byte("repos: []\n"),
		0o644,
	))

	checks, err := Load(dir)
	require.NoError(t, err)
	require.Len(t, checks, 1)
	assert.Equal(t, "pre-commit", checks[0].Name)
	assert.Equal(t, "pre-commit run --all-files", checks[0].Cmd)
}

func TestLoad_GitspiceYAMLBeatsPreCommitFramework(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".gitspice"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".gitspice", "precommit.yaml"),
		[]byte("checks:\n  - name: only\n    cmd: echo only\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".pre-commit-config.yaml"),
		[]byte("repos: []\n"),
		0o644,
	))

	checks, err := Load(dir)
	require.NoError(t, err)
	require.Len(t, checks, 1)
	assert.Equal(t, "only", checks[0].Name)
}

func TestLoad_PreCommitFrameworkBeatsMise(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".pre-commit-config.yaml"),
		[]byte("repos: []\n"),
		0o644,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "mise.toml"),
		[]byte("[tasks.lint]\nrun = \"go vet\"\n"),
		0o644,
	))

	checks, err := Load(dir)
	require.NoError(t, err)
	require.Len(t, checks, 1)
	assert.Equal(t, "pre-commit", checks[0].Name)
}

func TestLoad_BadTimeout(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".gitspice"), 0o755))

	yaml := `checks:
  - name: test
    cmd: go test ./...
    timeout: not-a-duration
`
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".gitspice", "precommit.yaml"),
		[]byte(yaml),
		0o644,
	))

	_, err := Load(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "test")
	assert.Contains(t, err.Error(), "not-a-duration")
}
