package claude

import (
	"bufio"
	"context"
	"encoding/json"
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

// _allowedTools is the conservative allowlist passed to claude via
// --allowedTools. The fix session should be able to:
//
//   - Read source files for context (Read, Glob, Grep)
//   - Modify existing files (Edit, MultiEdit)
//   - Stage changes (Bash(git add:*))
//   - Commit via the curated git-spice layer (Bash(gs:*))
//   - Inspect via read-only git (Bash(git diff/log/show/status:*))
//   - Verify changes locally (Bash go/mise subset)
//
// Notable exclusions (deliberately not allowed):
//
//   - Bash(git commit:*) — commits go through `gs cc` so they
//     trigger the right git-spice flows (upstack restack, etc.).
//   - Write — discourages creating new files; the agent should Edit
//     existing ones unless a comment explicitly asks for a new file.
//   - Bash(curl|wget|...) — no network tools.
//   - Bash(rm|mv|cp|...) — no destructive filesystem ops.
//   - WebFetch / WebSearch — no external lookups.
//   - Bash(git push:*) — pushing is git-spice's job, not the agent's.
//   - Bash(git rebase:*) — restacking is git-spice's job.
//
// Broaden this only when a real need surfaces in usage; keeping the
// surface small reduces both blast radius and accidental scope drift.
var _allowedTools = []string{
	"Read",
	"Edit",
	"MultiEdit",
	"Glob",
	"Grep",
	"Bash(gs:*)",
	"Bash(git add:*)",
	"Bash(git diff:*)",
	"Bash(git log:*)",
	"Bash(git show:*)",
	"Bash(git status:*)",
	"Bash(go build:*)",
	"Bash(go test:*)",
	"Bash(go vet:*)",
	"Bash(mise run:*)",
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
		// Tight allowlist: read code, edit code, run focused
		// git/go/mise commands. Nothing else — no curl, no rm, no
		// network tools, no broad shell access. Anything outside
		// this list will be blocked (and surfaced via the stream).
		// Keep this conservative; broaden only when a specific need
		// shows up in real usage.
		"--allowedTools", strings.Join(_allowedTools, " "),
		// Stream events as JSON so we can surface live progress
		// (tool calls, text deltas) instead of waiting for a final
		// result. --verbose is required by claude when stream-json
		// is combined with --print.
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--verbose",
		"-p", string(prompt),
	}
	cmd := xec.Command(ctx, log, bin, args...).
		WithDir(s.RepoRoot).
		WithStderr(s.Stderr)

	out := s.Stderr
	if out == nil {
		out = io.Discard
	}
	fmt.Fprintln(out, "→ Spawning Claude session…")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}

	// Drain the stream-json output in a goroutine, surfacing
	// human-readable progress (Edit/Bash/Read/etc.) as events arrive.
	// This replaces the old heartbeat — the stream IS the heartbeat.
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		streamEvents(stdout, out)
	}()

	runErr := cmd.Wait()
	<-streamDone
	duration := time.Since(start)
	fmt.Fprintf(out, "← Claude session finished in %s\n",
		duration.Round(time.Second))

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

// streamEvents reads claude's stream-json output line-by-line and
// prints human-readable progress lines to out. The format claude
// emits is one JSON object per line; we extract tool-use events
// (Edit / Bash / Read / etc.) as those are the most informative
// signals about what the agent is doing.
//
// Unrecognized event types are ignored silently — claude's stream
// schema evolves, and we don't want to spam the user with noise
// for events that don't help them.
func streamEvents(r io.Reader, out io.Writer) {
	scanner := bufio.NewScanner(r)
	// Some claude messages can be large; bump the line buffer.
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev streamEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		describeEvent(out, ev)
	}
}

// streamEvent is the subset of claude's stream-json schema we care
// about. Fields we don't need are ignored by encoding/json.
type streamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype,omitempty"`
	Message *struct {
		Role    string               `json:"role,omitempty"`
		Content []streamContentBlock `json:"content,omitempty"`
		Stop    string               `json:"stop_reason,omitempty"`
		Usage   map[string]any       `json:"usage,omitempty"`
	} `json:"message,omitempty"`
	Result string `json:"result,omitempty"`
}

type streamContentBlock struct {
	Type  string         `json:"type"`
	Text  string         `json:"text,omitempty"`
	Name  string         `json:"name,omitempty"`  // tool name on tool_use
	Input map[string]any `json:"input,omitempty"` // tool input on tool_use
}

// describeEvent writes a one-line human description of an event to
// out, when the event is one we recognize. Most noise (hooks, init,
// system internals) is skipped.
func describeEvent(out io.Writer, ev streamEvent) {
	if ev.Message == nil {
		return
	}
	for _, c := range ev.Message.Content {
		switch c.Type {
		case "tool_use":
			fmt.Fprintf(out, "  ▸ %s\n", describeToolUse(c))
		case "text":
			// Print short text snippets (skip very long ones to
			// avoid spamming during reasoning passes).
			t := strings.TrimSpace(c.Text)
			if t == "" {
				continue
			}
			if len(t) > 200 {
				t = t[:197] + "…"
			}
			fmt.Fprintf(out, "  · %s\n", t)
		}
	}
}

// describeToolUse formats a tool_use block into a one-line summary,
// e.g. "Edit branch_reviews.go" or "Bash: git commit -m \"...\"".
func describeToolUse(c streamContentBlock) string {
	switch c.Name {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		if path, ok := c.Input["file_path"].(string); ok {
			return fmt.Sprintf("%s %s", c.Name, path)
		}
	case "Read":
		if path, ok := c.Input["file_path"].(string); ok {
			return "Read " + path
		}
	case "Bash":
		if cmd, ok := c.Input["command"].(string); ok {
			if len(cmd) > 80 {
				cmd = cmd[:77] + "…"
			}
			return "Bash: " + cmd
		}
	case "Glob", "Grep":
		if pattern, ok := c.Input["pattern"].(string); ok {
			return fmt.Sprintf("%s %q", c.Name, pattern)
		}
	}
	return c.Name
}
