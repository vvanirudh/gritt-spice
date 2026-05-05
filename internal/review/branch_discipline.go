package review

import (
	"context"
	"fmt"
	"strings"

	"go.abhg.dev/gs/internal/silog"
	"go.abhg.dev/gs/internal/xec"
)

// FileLastBranch returns the name of the branch in branches (in
// the order given, trunk-to-tip) that most recently committed the
// file. If the file is unknown to git or touched only outside the
// listed branches, returns "".
//
// Uses "git log -1 -- <file>" scoped to each branch's range
// (parent-exclusive) to determine ownership. The returned name is
// the deepest (closest-to-tip) branch that has a commit touching
// the file in its own range — i.e. not inherited from a base.
func FileLastBranch(
	ctx context.Context,
	log *silog.Logger,
	repoRoot, file string,
	branches []string,
) (string, error) {
	if log == nil {
		log = silog.Nop()
	}
	for i := len(branches) - 1; i >= 0; i-- {
		branch := branches[i]

		args := []string{"log", "--pretty=format:%H", "-1"}
		if i > 0 {
			// Limit log to commits in this branch not in its parent.
			args = append(args, branches[i-1]+".."+branch)
		} else {
			args = append(args, branch)
		}
		args = append(args, "--", file)

		out, err := xec.Command(ctx, log, "git", args...).
			WithDir(repoRoot).
			Output()
		if err != nil {
			return "", fmt.Errorf("git log on %s: %w", branch, err)
		}
		if strings.TrimSpace(string(out)) != "" {
			return branch, nil
		}
	}
	return "", nil
}

// PreflightRestack returns the names of upper branches that would
// likely conflict if a base-branch fix were applied and then the
// stack restacked. Best-effort using "git merge-tree --write-tree".
//
// merge-tree emits "CONFLICT" in its output when
// a 3-way merge would conflict. We use that as the heuristic.
//
// This is a static pre-flight: we simulate merging baseBranch into
// each upper branch right now. The actual fix the agent makes might
// or might not trigger this conflict; this is an over-approximation
// to surface "this file is heavily modified upstack" cases.
func PreflightRestack(
	ctx context.Context,
	log *silog.Logger,
	repoRoot, baseBranch string,
	upperBranches []string,
) ([]string, error) {
	if log == nil {
		log = silog.Nop()
	}
	var conflicts []string
	for _, upper := range upperBranches {
		out, err := xec.Command(ctx, log,
			"git", "merge-tree", "--write-tree", baseBranch, upper,
		).
			WithDir(repoRoot).
			Output()
		if err != nil {
			// merge-tree returns non-zero on conflicts in newer git.
			// Inspect output regardless.
			if !strings.Contains(string(out), "CONFLICT") {
				log.Warn(
					"preflight merge-tree failed without conflict markers",
					"branch", upper,
					"err", err,
				)
				continue
			}
		}
		if strings.Contains(string(out), "CONFLICT") {
			conflicts = append(conflicts, upper)
		}
	}
	return conflicts, nil
}
