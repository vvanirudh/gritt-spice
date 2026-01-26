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

// maxOutputSize is the maximum size of stdout/stderr buffers (10 MB).
// This prevents memory exhaustion from malicious or runaway CLI output.
const maxOutputSize = 10 * 1024 * 1024

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

// SendPrompt sends a prompt to Claude and returns the response.
// Uses the default model.
func (c *Client) SendPrompt(ctx context.Context, prompt string) (string, error) {
	return c.SendPromptWithModel(ctx, prompt, "")
}

// SendPromptWithModel sends a prompt to Claude with a specific model.
// If model is empty, uses Claude's default model.
func (c *Client) SendPromptWithModel(ctx context.Context, prompt, model string) (string, error) {
	binaryPath, err := c.resolveBinaryPath()
	if err != nil {
		return "", err
	}

	// Validate model name to prevent injection attacks.
	if model != "" && !isValidModelName(model) {
		return "", fmt.Errorf("invalid model name: %q", model)
	}

	// Apply timeout to prevent indefinite hangs.
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	// Prepare command with -p flag for prompt and --print for non-interactive mode.
	// The --print flag ensures the CLI outputs the response without interactive prompts.
	args := []string{"-p", prompt, "--print"}
	if model != "" {
		args = append(args, "--model", model)
	}
	cmd := xec.Command(ctx, c.log, binaryPath, args...)

	// Use limited buffers to prevent memory exhaustion.
	stdout := &limitedBuffer{limit: maxOutputSize}
	stderr := &limitedBuffer{limit: maxOutputSize}
	cmd = cmd.WithStdout(stdout).WithStderr(stderr)

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

	return strings.TrimSpace(stdout.String()), nil
}

// checkStderr checks for known error patterns in stderr output.
//
// Error detection is based on string matching against common error messages
// from the Claude CLI. These patterns are based on observed CLI behavior
// (tested with Claude CLI v1.x) and may need updates if the CLI changes
// its error message format. The CLI does not currently provide structured
// error output, so we rely on substring matching.
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

// isValidModelName checks if a model name contains only safe characters.
// Model names should be alphanumeric with hyphens, underscores, and dots.
func isValidModelName(model string) bool {
	if model == "" {
		return false
	}
	for _, r := range model {
		isLower := r >= 'a' && r <= 'z'
		isUpper := r >= 'A' && r <= 'Z'
		isDigit := r >= '0' && r <= '9'
		isSpecial := r == '-' || r == '_' || r == '.'
		if !isLower && !isUpper && !isDigit && !isSpecial {
			return false
		}
	}
	return true
}

// IsAvailable checks if the Claude CLI is available.
func (c *Client) IsAvailable() bool {
	_, err := c.resolveBinaryPath()
	return err == nil
}

// resolveBinaryPath resolves the Claude binary path, caching the result.
// Thread-safety: This method is safe for concurrent use. The sync.Once
// ensures the binary lookup is performed exactly once, regardless of how
// many goroutines call this method concurrently. Subsequent calls return
// the cached result immediately.
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

// limitedBuffer is a buffer that stops accepting writes after reaching a limit.
// It silently discards data beyond the limit to prevent memory exhaustion.
//
// IMPORTANT: This type intentionally violates the io.Writer contract by returning
// len(p), nil even when data is discarded. This is by design to prevent callers
// (like exec.Cmd) from treating truncation as an error. Use only for scenarios
// where silent truncation is acceptable, such as capturing CLI output.
type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

// Write implements io.Writer with a size limit.
// Returns len(p), nil always to indicate success, even when data is discarded.
// This intentionally violates the io.Writer contract to prevent callers from
// treating truncation as an error.
func (b *limitedBuffer) Write(p []byte) (n int, err error) {
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		// At limit: silently discard all data.
		return len(p), nil
	}
	if len(p) <= remaining {
		// Fits entirely: write all.
		_, err = b.buf.Write(p)
		return len(p), err
	}
	// Partial fit: write what we can, report full success.
	_, err = b.buf.Write(p[:remaining])
	return len(p), err
}

// String returns the buffered content.
func (b *limitedBuffer) String() string {
	return b.buf.String()
}

// Len returns the current buffer length.
func (b *limitedBuffer) Len() int {
	return b.buf.Len()
}
