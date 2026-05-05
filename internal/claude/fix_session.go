package claude

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"go.abhg.dev/gs/internal/silog"
	"go.abhg.dev/gs/internal/xec"
)

// FixSession spawns a focused claude Code session against an
// extracted plugin and a written INSTRUCTIONS.md.
type FixSession struct {
	// PluginDir is the path returned by plugin.ExtractPullAndAddress.
	PluginDir string

	// Instructions is the path to INSTRUCTIONS.md (typically inside
	// PluginDir). The plugin's CLAUDE.md tells the session to read it.
	Instructions string

	// RepoRoot is the working directory for the spawned claude.
	RepoRoot string

	// Stdout/Stderr receive the session's output.
	Stdout io.Writer
	Stderr io.Writer

	// Log is used for diagnostic logging by FixSession itself
	// (NOT the spawned claude process).
	Log *silog.Logger

	// ClaudeBinary overrides the binary path. Defaults to "claude".
	// Tests inject a fake script.
	ClaudeBinary string
}

// FixResult summarizes what the session did.
type FixResult struct {
	// NewCommits are the SHAs (oldest-first) created during the
	// session, found via `git rev-list --reverse oldHEAD..HEAD`.
	NewCommits []string

	// PerItem maps an Addresses-# id to the SHAs that mention it.
	// One commit can mention multiple ids; one id can be claimed
	// by multiple commits.
	PerItem map[string][]string

	// Unmatched are commit SHAs that contain no Addresses-# marker.
	Unmatched []string

	// Aborted is true if the spawned claude exited non-zero.
	// Commits made before the abort still count in NewCommits.
	Aborted bool

	// Duration is wall-clock time of the spawned process.
	Duration time.Duration
}

// addressesRE matches the marker the spawned Claude session is told
// to include in commit bodies. It accepts "addresses", "fixes", or
// "resolves" (case-insensitive) followed by something containing an
// "#<id>" reference.
var addressesRE = regexp.MustCompile(
	`(?i)\b(?:addresses|fixes|resolves)\b[^#]*#([a-zA-Z0-9_-]+)`,
)

// Run spawns claude with the plugin and waits for it to exit.
// After exit it walks new commits and parses markers.
func (s *FixSession) Run(ctx context.Context) (*FixResult, error) {
	bin := s.ClaudeBinary
	if bin == "" {
		bin = "claude"
	}
	log := s.Log
	if log == nil {
		log = silog.Nop()
	}

	oldHead, err := gitRevParseHead(ctx, log, s.RepoRoot)
	if err != nil {
		return nil, fmt.Errorf("rev-parse HEAD: %w", err)
	}

	// Read the instructions file and pass its content as the prompt
	// to claude in non-interactive (-p) mode. Without -p, claude
	// would launch an interactive REPL and never return.
	prompt, err := os.ReadFile(s.Instructions)
	if err != nil {
		return nil, fmt.Errorf("read instructions: %w", err)
	}

	args := []string{
		"--plugin-dir", s.PluginDir,
		"-p", string(prompt),
	}
	cmd := xec.Command(ctx, log, bin, args...).
		WithDir(s.RepoRoot).
		WithStdout(s.Stdout).
		WithStderr(s.Stderr)

	start := time.Now()
	runErr := cmd.Run()
	duration := time.Since(start)

	res := &FixResult{
		PerItem:  make(map[string][]string),
		Duration: duration,
	}

	commits, err := gitRevList(ctx, log, s.RepoRoot, oldHead, "HEAD")
	if err != nil {
		return res, fmt.Errorf("rev-list: %w", err)
	}
	res.NewCommits = commits

	for _, sha := range commits {
		body, err := gitCommitBody(ctx, log, s.RepoRoot, sha)
		if err != nil {
			return res, fmt.Errorf("commit body %s: %w", sha, err)
		}
		matches := addressesRE.FindAllStringSubmatch(body, -1)
		if len(matches) == 0 {
			res.Unmatched = append(res.Unmatched, sha)
			continue
		}
		for _, m := range matches {
			id := m[1]
			res.PerItem[id] = append(res.PerItem[id], sha)
		}
	}

	if runErr != nil {
		res.Aborted = true
		return res, fmt.Errorf("claude session: %w", runErr)
	}
	return res, nil
}

func gitRevParseHead(
	ctx context.Context,
	log *silog.Logger,
	repo string,
) (string, error) {
	out, err := xec.Command(ctx, log, "git", "rev-parse", "HEAD").
		WithDir(repo).
		Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitRevList(
	ctx context.Context,
	log *silog.Logger,
	repo, from, to string,
) ([]string, error) {
	out, err := xec.Command(
		ctx, log, "git", "rev-list", "--reverse", from+".."+to,
	).
		WithDir(repo).
		Output()
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, nil
	}
	return strings.Split(trimmed, "\n"), nil
}

func gitCommitBody(
	ctx context.Context,
	log *silog.Logger,
	repo, sha string,
) (string, error) {
	out, err := xec.Command(
		ctx, log, "git", "log", "-1", "--format=%B", sha,
	).
		WithDir(repo).
		Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// CommitSubject returns the one-line subject of the given commit SHA
// using `git log -1 --format=%s`. It is exported so other packages
// (e.g. internal/review) can look up commit subjects without
// duplicating the xec logic.
func CommitSubject(
	ctx context.Context,
	log *silog.Logger,
	repo, sha string,
) (string, error) {
	out, err := xec.Command(
		ctx, log, "git", "log", "-1", "--format=%s", sha,
	).
		WithDir(repo).
		Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
