package claude

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"go.abhg.dev/gs/internal/silog"
	"go.abhg.dev/gs/internal/xec"
)

// Sentinel errors for Claude client operations.
var (
	// ErrNotInstalled indicates the Claude CLI is not installed.
	ErrNotInstalled = errors.New("claude CLI not installed")

	// ErrNotAuthenticated indicates the user needs to authenticate.
	ErrNotAuthenticated = errors.New("not authenticated with Claude")

	// ErrRateLimited indicates the API rate limit was exceeded.
	ErrRateLimited = errors.New("rate limit exceeded")
)

// Error represents an error from the Claude CLI.
type Error struct {
	Message string
}

func (e *Error) Error() string {
	return "claude: " + e.Message
}

// ClientOptions configures the Claude client.
type ClientOptions struct {
	// BinaryPath is the path to the claude binary.
	// If empty, the client will search for it in PATH.
	BinaryPath string

	// Timeout is the maximum duration for Claude API calls.
	// If zero, DefaultTimeout is used.
	Timeout time.Duration

	// Log is the logger to use. Optional.
	Log *silog.Logger
}

// Client wraps the Claude CLI for AI operations.
type Client struct {
	binaryPath string
	timeout    time.Duration
	log        *silog.Logger

	// binaryOnce ensures binary path is resolved only once.
	binaryOnce sync.Once
	// resolvedPath is the cached binary path after resolution.
	resolvedPath string
	// resolveErr is the cached error from binary resolution.
	resolveErr error
}

// NewClient creates a new Claude client.
func NewClient(opts *ClientOptions) *Client {
	if opts == nil {
		opts = &ClientOptions{}
	}
	log := opts.Log
	if log == nil {
		log = silog.Nop()
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	return &Client{
		binaryPath: opts.BinaryPath,
		timeout:    timeout,
		log:        log,
	}
}

// FindClaudeBinary searches for the claude binary in PATH.
func FindClaudeBinary() (string, error) {
	path, err := xec.LookPath("claude")
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrNotInstalled, err)
	}
	return path, nil
}

// Run executes a prompt using the Claude CLI and returns the response.
// Uses the default model.
func (c *Client) Run(ctx context.Context, prompt string) (string, error) {
	return c.RunWithModel(ctx, prompt, "")
}

// RunWithModel executes a prompt using the Claude CLI with a specific model.
// If model is empty, uses Claude's default model.
func (c *Client) RunWithModel(ctx context.Context, prompt, model string) (string, error) {
	binaryPath, err := c.resolveBinaryPath()
	if err != nil {
		return "", err
	}

	// Apply timeout to prevent indefinite hangs.
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// Prepare command with -p flag for prompt.
	args := []string{"-p", prompt}
	if model != "" {
		args = append(args, "--model", model)
	}
	cmd := xec.Command(ctx, c.log, binaryPath, args...)

	var stdout, stderr bytes.Buffer
	cmd = cmd.WithStdout(&stdout).WithStderr(&stderr)

	err = cmd.Run()
	if err != nil {
		// Check stderr for known error patterns.
		if stderrErr := checkStderr(stderr.String()); stderrErr != nil {
			return "", stderrErr
		}
		// If stderr is empty, check if stdout has error info.
		if stderr.Len() == 0 && stdout.Len() > 0 {
			output := strings.TrimSpace(stdout.String())
			// Limit output length for readability.
			if len(output) > 200 {
				output = output[:200] + "..."
			}
			return "", &Error{Message: output}
		}
		return "", &Error{Message: err.Error()}
	}

	return parseResponse(stdout.String()), nil
}

// parseResponse cleans up the raw Claude CLI output.
func parseResponse(output string) string {
	return strings.TrimSpace(output)
}

// checkStderr checks for known error patterns in stderr output.
//
// Error detection is based on string matching against common error messages
// from the Claude CLI. These patterns are based on observed CLI behavior
// and may need updates if the CLI changes its error message format.
// The CLI does not currently provide structured error output.
func checkStderr(stderr string) error {
	stderrLower := strings.ToLower(stderr)

	// Authentication errors: CLI prompts user to run 'claude auth'.
	if strings.Contains(stderrLower, "not authenticated") ||
		strings.Contains(stderrLower, "please run 'claude auth'") ||
		strings.Contains(stderrLower, "authentication") {
		return ErrNotAuthenticated
	}

	// Rate limit errors: API returns 429 or similar.
	if strings.Contains(stderrLower, "rate limit") ||
		strings.Contains(stderrLower, "too many requests") {
		return ErrRateLimited
	}

	// Any other stderr output is treated as an error.
	if stderr != "" {
		return &Error{Message: strings.TrimSpace(stderr)}
	}

	return nil
}

// IsAvailable checks if the Claude CLI is available.
func (c *Client) IsAvailable() bool {
	_, err := c.resolveBinaryPath()
	return err == nil
}

// resolveBinaryPath resolves the Claude binary path, caching the result.
// This is thread-safe and will only perform the lookup once.
func (c *Client) resolveBinaryPath() (string, error) {
	c.binaryOnce.Do(func() {
		path := c.binaryPath
		if path == "" {
			path, c.resolveErr = FindClaudeBinary()
			if c.resolveErr != nil {
				return
			}
		}

		// Verify binary exists.
		if _, err := os.Stat(path); err != nil {
			c.resolveErr = fmt.Errorf("%w: %w", ErrNotInstalled, err)
			return
		}

		c.resolvedPath = path
	})

	return c.resolvedPath, c.resolveErr
}
