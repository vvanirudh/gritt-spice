package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.abhg.dev/gs/internal/claude"
	"go.abhg.dev/gs/internal/git"
	"go.abhg.dev/gs/internal/handler/restack"
	"go.abhg.dev/gs/internal/silog"
	"go.abhg.dev/gs/internal/text"
	"go.abhg.dev/gs/internal/ui"
)

type commitCreateCmd struct {
	All           bool   `short:"a" help:"Stage all changes before committing."`
	AllowEmpty    bool   `help:"Create a new commit even if it contains no changes."`
	ClaudeSummary bool   `help:"Generate commit message using Claude AI."`
	Fixup         string `help:"Create a fixup commit. See also 'gs commit fixup'." placeholder:"COMMIT"`
	Message       string `short:"m" placeholder:"MSG" help:"Use the given message as the commit message."`
	NoVerify      bool   `help:"Bypass pre-commit and commit-msg hooks."`
	Signoff       bool   `config:"commit.signoff" help:"Add Signed-off-by trailer to the commit message"`
}

func (*commitCreateCmd) Help() string {
	return text.Dedent(`
		Staged changes are committed to the current branch.
		Branches upstack are restacked if necessary.
		Use this as a shortcut for 'git commit'
		followed by 'gs upstack restack'.

		An editor is opened to edit the commit message.
		Use the -m/--message option to specify the message
		without opening an editor.
		Git hooks are run unless the --no-verify flag is given.

		Use the -a/--all flag to stage all changes before committing.

		Use the --fixup flag to create a new commit that will be merged
		into another commit when run with 'git rebase --autosquash'.
		See also, the 'gs commit fixup' command, which is preferable
		when you want to apply changes to an older commit.
	`)
}

func (cmd *commitCreateCmd) Run(
	ctx context.Context,
	log *silog.Logger,
	view ui.View,
	wt *git.Worktree,
	restackHandler RestackHandler,
) error {
	// Generate commit message with Claude if requested.
	message := cmd.Message
	if cmd.ClaudeSummary && message == "" {
		var err error
		message, err = cmd.generateCommitMessage(ctx, log, view, wt)
		if err != nil {
			return fmt.Errorf("generate commit message: %w", err)
		}
		if message == "" {
			return errors.New("commit cancelled")
		}
	}

	if err := wt.Commit(ctx, git.CommitRequest{
		Message:    message,
		All:        cmd.All,
		AllowEmpty: cmd.AllowEmpty,
		Fixup:      cmd.Fixup,
		NoVerify:   cmd.NoVerify,
		Signoff:    cmd.Signoff,
	}); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	if _, err := wt.RebaseState(ctx); err == nil {
		// In the middle of a rebase.
		// Don't restack upstack branches.
		log.Debug("A rebase is in progress, skipping restack")
		return nil
	}

	currentBranch, err := wt.CurrentBranch(ctx)
	if err != nil {
		// No restack needed if we're in a detached head state.
		if errors.Is(err, git.ErrDetachedHead) {
			log.Debug("HEAD is detached, skipping restack")
			return nil
		}
		return fmt.Errorf("get current branch: %w", err)
	}

	return restackHandler.RestackUpstack(ctx, currentBranch, &restack.UpstackOptions{
		SkipStart: true,
	})
}

func (cmd *commitCreateCmd) generateCommitMessage(
	ctx context.Context,
	log *silog.Logger,
	view ui.View,
	wt *git.Worktree,
) (string, error) {
	// Check Claude availability.
	client := claude.NewClient(nil)
	if !client.IsAvailable() {
		return "", errors.New("claude CLI not found; please install it")
	}

	// Load configuration.
	cfg, err := claude.LoadConfig(claude.DefaultConfigPath())
	if err != nil {
		log.Warn("Could not load claude config, using defaults", "error", err)
		cfg = claude.DefaultConfig()
	}

	// Get staged diff.
	diffText, err := wt.DiffStaged(ctx)
	if err != nil {
		return "", fmt.Errorf("get staged diff: %w", err)
	}

	if diffText == "" {
		return "", errors.New("no staged changes")
	}

	// Parse and filter the diff.
	files, err := claude.ParseDiff(diffText)
	if err != nil {
		return "", fmt.Errorf("parse diff: %w", err)
	}

	filtered := claude.FilterDiff(files, cfg.IgnorePatterns)
	if len(filtered) == 0 {
		return "", errors.New("no changes to commit after filtering")
	}

	// Check budget.
	budget := claude.CheckBudget(filtered, cfg.MaxLines)
	if budget.OverBudget {
		return "", fmt.Errorf("diff too large (%d lines, max %d)",
			budget.TotalLines, budget.MaxLines)
	}

	// Build prompt and run.
	filteredDiff := claude.ReconstructDiff(filtered)
	prompt := claude.BuildCommitPrompt(cfg, filteredDiff)

	log.Info("Generating commit message with Claude...")
	response, err := client.Run(ctx, prompt)
	if err != nil {
		return "", err
	}

	// Parse the response to extract subject and body.
	subject, body := parseCommitResponse(response)

	// Show preview and get user choice.
	return showCommitPreview(view, subject, body)
}

// parseCommitResponse extracts subject and body from Claude's response.
func parseCommitResponse(response string) (subject, body string) {
	lines := strings.Split(strings.TrimSpace(response), "\n")
	if len(lines) == 0 {
		return response, ""
	}

	// Look for SUBJECT: or TITLE: prefix.
	for i, line := range lines {
		lineLower := strings.ToLower(line)
		if strings.HasPrefix(lineLower, "subject:") {
			subject = strings.TrimSpace(strings.TrimPrefix(line, line[:8]))
			if i+1 < len(lines) {
				// Skip empty lines and BODY: prefix.
				remaining := lines[i+1:]
				for j, l := range remaining {
					lLower := strings.ToLower(l)
					if strings.HasPrefix(lLower, "body:") {
						remaining = remaining[j+1:]
						break
					}
					if strings.TrimSpace(l) != "" {
						remaining = remaining[j:]
						break
					}
				}
				body = strings.TrimSpace(strings.Join(remaining, "\n"))
			}
			return subject, body
		}
	}

	// Fallback: first line is subject, rest is body.
	subject = lines[0]
	if len(lines) > 1 {
		body = strings.TrimSpace(strings.Join(lines[1:], "\n"))
	}
	return subject, body
}

// showCommitPreview shows the generated commit message and lets user accept/edit.
func showCommitPreview(
	view ui.View,
	subject, body string,
) (string, error) {
	// For non-interactive mode, just return the message.
	if !ui.Interactive(view) {
		if body != "" {
			return subject + "\n\n" + body, nil
		}
		return subject, nil
	}

	// Show preview.
	fmt.Fprintln(view, "")
	fmt.Fprintln(view, "=== Claude suggests ===")
	fmt.Fprintln(view, "Subject:", subject)
	if body != "" {
		fmt.Fprintln(view, "")
		fmt.Fprintln(view, body)
	}
	fmt.Fprintln(view, "=======================")
	fmt.Fprintln(view, "")

	// Ask for confirmation.
	type choice int
	const (
		choiceAccept choice = iota
		choiceEdit
		choiceCancel
	)

	var selected choice
	field := ui.NewSelect[choice]().
		WithTitle("Action").
		WithValue(&selected).
		WithOptions(
			ui.SelectOption[choice]{Label: "Accept", Value: choiceAccept},
			ui.SelectOption[choice]{Label: "Edit in $EDITOR", Value: choiceEdit},
			ui.SelectOption[choice]{Label: "Cancel", Value: choiceCancel},
		)

	if err := ui.Run(view, field); err != nil {
		return "", err
	}

	message := subject
	if body != "" {
		message = subject + "\n\n" + body
	}

	switch selected {
	case choiceAccept:
		return message, nil
	case choiceEdit:
		// Return the message so git's editor can be used.
		// The -m flag with the generated message will open the editor.
		return message, nil
	case choiceCancel:
		return "", nil
	}

	return message, nil
}
