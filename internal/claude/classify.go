package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Item is one classifiable thing — a review thread or a CI check.
type Item struct {
	Kind      string // "review-thread" | "check"
	Title     string // short context (e.g. "branch_submit.go:142 — copilot[bot]")
	Body      string // the comment text or check failure log
	File      string // file path the item is anchored to (may be "")
	LineRange [2]int // [start, end]; 0,0 if not anchored
	Hunk      string // diff hunk for context (may be "")
	Author    string // raw login including [bot] suffix
	Source    any    // *forge.ReviewThreadItem or *forge.ChangeCheckItem
}

// Classification is the structured response from ClassifyItem.
type Classification struct {
	Category        string // "nit"|"bug"|"question"|"suggestion"|"out-of-scope"|"unclassified"
	Summary         string
	SuggestedAction string // "edit" | "reply" | "defer"
	ReplyDraft      string
	FixPlan         string
	Complexity      string // "trivial" | "small" | "medium" | "large"
}

// PromptRunner abstracts the call to Claude for testability.
// Production uses defaultPromptRunner which wraps the Client.
type PromptRunner interface {
	Run(ctx context.Context, cfg Config, prompt string) (string, error)
}

// ClassifyItem runs a one-shot prompt through Claude and parses the
// structured JSON response. On a malformed response, retries once.
// On a second malformed response, returns Category: "unclassified"
// (not an error — the caller can render a manual category select).
//
// On a real Claude error (network, binary missing), returns the error
// wrapped via RunClaudeError.
func ClassifyItem(
	ctx context.Context,
	cfg Config,
	item *Item,
) (*Classification, error) {
	return ClassifyItemWithRunner(ctx, cfg, item, defaultPromptRunner{})
}

// ClassifyItemWithRunner is exposed so tests can inject a fake runner.
func ClassifyItemWithRunner(
	ctx context.Context,
	cfg Config,
	item *Item,
	runner PromptRunner,
) (*Classification, error) {
	prompt := buildClassifyPrompt(item)
	for range 2 {
		resp, err := runner.Run(ctx, cfg, prompt)
		if err != nil {
			return nil, RunClaudeError(err)
		}
		c, parseErr := parseClassification(resp)
		if parseErr == nil {
			return c, nil
		}
	}
	return &Classification{Category: "unclassified"}, nil
}

// defaultPromptRunner is the production implementation, wrapping
// claude.Client.SendPromptWithModel using cfg.Models.Review.
type defaultPromptRunner struct{}

func (defaultPromptRunner) Run(
	ctx context.Context,
	cfg Config,
	prompt string,
) (string, error) {
	c := NewClient(nil)
	if !c.IsAvailable() {
		return "", errors.New("claude CLI not available")
	}
	return c.SendPromptWithModel(ctx, prompt, cfg.Models.Review)
}

func buildClassifyPrompt(item *Item) string {
	return fmt.Sprintf(
		`You are classifying a code-review item to drive an automated address-and-reply workflow.

Respond with strict JSON ONLY. No prose. No markdown fences.

Schema:
{
  "category": "nit" | "bug" | "question" | "suggestion" | "out-of-scope",
  "summary": "<one sentence>",
  "suggested_action": "edit" | "reply" | "defer",
  "reply_draft": "<short reply text>",
  "fix_plan": "<2-3 line plan; empty string if no edit needed>",
  "complexity": "trivial" | "small" | "medium" | "large"
}

Item kind: %s
Author: %s
File: %s
Line range: %d-%d
Body:
%s

Diff hunk for context:
%s
`,
		item.Kind,
		item.Author,
		item.File,
		item.LineRange[0],
		item.LineRange[1],
		item.Body,
		item.Hunk,
	)
}

type classificationJSON struct {
	Category        string `json:"category"`
	Summary         string `json:"summary"`
	SuggestedAction string `json:"suggested_action"`
	ReplyDraft      string `json:"reply_draft"`
	FixPlan         string `json:"fix_plan"`
	Complexity      string `json:"complexity"`
}

func parseClassification(resp string) (*Classification, error) {
	var c classificationJSON
	if err := json.Unmarshal([]byte(resp), &c); err != nil {
		return nil, err
	}
	if c.Category == "" {
		return nil, errors.New("missing category")
	}
	return &Classification{
		Category:        c.Category,
		Summary:         c.Summary,
		SuggestedAction: c.SuggestedAction,
		ReplyDraft:      c.ReplyDraft,
		FixPlan:         c.FixPlan,
		Complexity:      c.Complexity,
	}, nil
}
