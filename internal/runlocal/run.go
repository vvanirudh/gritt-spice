// Package runlocal runs configured local commands (lint/test/build)
// and captures their output for use by the pull-and-address fix-flow.
package runlocal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// Check is one command to execute.
type Check struct {
	// Name is the human-readable identifier for the check.
	Name string

	// Cmd is the shell command line to execute (run via sh -c).
	Cmd string

	// Dir is the working directory for the command.
	// If empty, the parent process's working directory is used.
	Dir string

	// FailFast stops the run if this check fails.
	FailFast bool

	// Timeout cancels the check if it exceeds this duration.
	// A zero value means no timeout.
	Timeout time.Duration
}

// Result is the outcome of running a Check.
type Result struct {
	// Name is the Name field of the corresponding Check.
	Name string

	// Cmd is the shell command that was executed.
	// Mirrored from the Check so callers that only see Results
	// (e.g. the --fix prompt) can include it in diagnostics.
	Cmd string

	// ExitCode is the process exit status. -1 if the process could
	// not be started or was killed before exiting normally.
	ExitCode int

	// Duration is wall-clock time the check took to run.
	Duration time.Duration

	// Output is the captured combined stdout+stderr of the check.
	Output string

	// Err is set when the process failed to start or was killed.
	// A non-zero ExitCode without Err indicates a normal exit.
	Err error
}

// Runner executes a sequence of Checks. The interface exists for
// testability; the production type is DefaultRunner.
type Runner interface {
	// Run executes the checks in order. It streams combined
	// stdout+stderr to out as each check runs and also captures
	// the same bytes per-check into Result.Output.
	//
	// All per-check outcomes are reported via Result fields:
	// non-zero ExitCode for a normal failed exit, ExitCode = -1
	// with Result.Err set for start failures or context cancellation.
	// The Go error return is reserved for future Runner-level failures
	// and is currently always nil.
	Run(ctx context.Context, checks []Check, out io.Writer) ([]Result, error)
}

// DefaultRunner is the production implementation of Runner.
// It executes checks sequentially using the system shell (sh -c).
type DefaultRunner struct{}

// Run executes checks in order, streaming combined stdout+stderr to out
// and capturing the same bytes per-check into Result.Output.
//
// All per-check outcomes are reported via Result fields rather than
// the Go error return: non-zero ExitCode for a normal failed exit,
// ExitCode = -1 with Result.Err set for start failures or context
// cancellation. The Go error return is reserved for future
// Runner-level failures and is currently always nil.
//
// If a check has FailFast set and exits non-zero,
// Run stops early and returns the partial results.
func (DefaultRunner) Run(
	ctx context.Context,
	checks []Check,
	out io.Writer,
) ([]Result, error) {
	var results []Result
	for _, check := range checks {
		result, err := runCheck(ctx, check, out)
		if err != nil {
			return results, err
		}
		results = append(results, result)
		if check.FailFast && result.ExitCode != 0 {
			break
		}
	}
	return results, nil
}

// runCheck executes a single check and returns its Result.
//
// The error return is reserved for future use; today it is always nil
// because all per-check failure modes are reported via Result fields:
// non-zero ExitCode for normal exits, ExitCode = -1 with Result.Err
// set for start failures or context cancellation. Returning the
// failure as a Result instead of a Go error lets DefaultRunner
// continue running subsequent checks (unless FailFast is set).
func runCheck(
	ctx context.Context,
	check Check,
	out io.Writer,
) (Result, error) {
	if check.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, check.Timeout)
		defer cancel()
	}

	fmt.Fprintf(out, "▶ %s: %s\n", check.Name, check.Cmd)

	var captured bytes.Buffer
	cmd := exec.CommandContext(ctx, "sh", "-c", check.Cmd)
	cmd.Dir = check.Dir
	// Platform-specific: install process-group control on Unix so we
	// can kill child processes spawned by the shell on context cancel.
	// Windows uses job objects and doesn't need this.
	setProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdout = io.MultiWriter(out, &captured)
	cmd.Stderr = cmd.Stdout

	start := time.Now()
	runErr := cmd.Run()
	duration := time.Since(start)

	result := Result{
		Name:     check.Name,
		Cmd:      check.Cmd,
		Duration: duration,
		Output:   captured.String(),
	}

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			// Process could not be started or was killed
			// without an exit code.
			result.ExitCode = -1
			result.Err = runErr
		}
	}

	return result, nil
}
