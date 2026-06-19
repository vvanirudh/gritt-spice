# git-spice

## Introduction

<div align="center">
  <img src="doc/src/img/logo.png" width="300"/>
</div>

[![CI](https://github.com/abhinav/git-spice/actions/workflows/ci.yml/badge.svg)](https://github.com/abhinav/git-spice/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/abhinav/git-spice/graph/badge.svg?token=FE4S370I4A)](https://codecov.io/gh/abhinav/git-spice)

</div>

git-spice is a tool for stacking Git branches.
It lets you manage and navigate stacks of branches,
conveniently modify and rebase them,
and create GitHub Pull Requests or GitLab Merge Requests from them.

See <https://abhinav.github.io/git-spice/> for more details.

Usage looks roughly like this:

```shell
# Stack a branch on top of the current branch.
$ gs branch create feat1

# Stack another branch on top of feat1.
$ gs branch create feat2

# Submit pull requests for feat1 and feat2.
$ gs stack submit

# Pull latest changes from the remote repository
# and delete merged branches.
$ gs repo sync

# Restack branches on top of the latest changes.
$ gs stack restack
```

Or equivalently, using [CLI shorthands](https://abhinav.github.io/git-spice/cli/shorthand/):

```shell
$ gs bc feat1  # branch create feat1
$ gs bc feat2  # branch create feat2
$ gs ss        # stack submit
$ gs rs        # repo sync
$ gs sr        # stack restack
```

## Features

- Create, edit, and navigate stacks of branches with ease.
- Submit the entire stack or parts of it with a single command.
  Supports GitHub and GitLab.
- Flexible restacking with support for both rebase and merge-based workflows.
- Keep using your existing workflow and adopt git-spice incrementally.
- Completely offline operation with no external dependencies
  until you push or pull from a remote repository.
- Easy-to-remember shorthands for most commands.

## Gritt-flavored additions

This fork (`gritt-spice`) layers AI-assisted commands and PR-review
surfacing on top of upstream git-spice. The upstream commands are
unchanged; everything below is additive.

### Claude-assisted commit messages and PR bodies

| Command | What it does |
|---|---|
| `gs commit create --claude-summary` (alias `gs cc --claude-summary`) | Generate the commit message via Claude using the staged diff. Falls through to the standard editor flow if Claude is unavailable. |
| `gs branch submit --claude-summary` | Generate the PR title and body via Claude using the branch's full diff against base. Skip with `--title=...` / `--body=...` for a manual override. |

### Code review with Claude

| Command | What it does |
|---|---|
| `gs claude review` | Review the changes between two refs (default: trunk → current branch) and print Claude's review to stderr. `--per-branch` reviews each branch in the stack individually then provides an overall summary. |

### Local CI runner

| Command | What it does |
|---|---|
| `gs run precommit-checks` | Run a list of locally-configured "what CI runs" commands before pushing. Reads `.gitspice/precommit.yaml` if present, then `.pre-commit-config.yaml` (delegates to `pre-commit run --all-files`), then auto-detects `mise.toml` tasks named `lint` / `test` / `build`, then falls back to hardcoded `mise run` invocations. |

`--only=name1,name2` filters to a subset; `--fix` hands captured
failure output to Claude for diagnosis. Runs each check pinned to the
repo root regardless of where the user invokes from, so behavior
matches CI even when run from a subdirectory.

### Read-only PR review surfacing

| Command | What it does |
|---|---|
| `gs branch reviews` | Fetch open review threads for the current branch's PR and print a per-file summary table (full reviewer body, word-wrapped). Read-only — addressing comments is the user's job. |
| `gs branch checks` | Fetch CI check runs for the current branch's PR and print a per-check status summary (failing only by default; `--include-passing` for everything). |
| `gs stack reviews` | Same as `gs branch reviews`, but iterates every branch in the current stack with per-branch headers. |
| `gs stack checks` | Same as `gs branch checks`, but iterates every branch in the current stack. |

Threads where the viewer's most recent reply matches `Addressed in
<sha>` are filtered out automatically, so you don't keep seeing
threads you've already addressed. Resolved threads are filtered by
default (`--include-resolved` to include them). Bot comments are
filtered to a small allowlist of AI review tools (Copilot, Claude,
Codex, GitHub Advanced Security) by default.

These commands are read-only and do not classify, address, reply, or
make commits. They surface what exists; you decide what to do with
your editor / IDE / claude session of choice.

### Stacked PR workflow notes

git-spice's stacked-PR model interacts with two GitHub repo settings
that Gritt has enabled:

1. **Squash merge** — combines a PR's commits into one commit on the
   target branch with a brand-new SHA.
2. **Automatically delete head branches** — removes the head branch
   on the remote after a merge.

Both settings are fine for non-stacked PRs but create friction when
an *intermediate* PR in a stack is merged: the upper PR's commits no
longer rebase cleanly onto the new main, and its base ref is gone.

**Symptom:** after merging an intermediate stacked PR (say `PR α →
PR β`), PR β's diff on GitHub looks like duplicates / weird history,
and its base ref appears as `<deleted>`.

**Fix — the post-merge sync sequence.** Run this in the worktree
holding the upper branch:

```bash
git fetch upstream main
gs repo sync          # detects PR α merged; deletes the local α branch
gs branch restack     # replays PR β's commits onto fresh main
gs branch submit      # force-pushes β; retargets the PR's base to main
```

`gs repo sync` will also print a hint per upstack branch that needs
restacking, e.g. `feat/X: moved upstack onto main (needs restack —
run 'gs branch restack' to rebase, then 'gs branch submit' to update
its PR)`.

**Avoiding the issue entirely.** When a set of changes can stand
alone (touches disjoint files), open each PR independently against
`main` rather than stacking. Stacking is only worth the friction
when later commits genuinely depend on earlier ones for their tests
or symbols.

### Requesting Copilot review on submit

GitHub's "Copilot Code Review" feature treats `Copilot` as a
requestable reviewer. To request it via gs, pass the bot login
through the existing `--reviewer` flag:

```bash
gs branch submit --reviewer copilot-pull-request-reviewer
```

Combine with human reviewers as usual:
`--reviewer copilot-pull-request-reviewer --reviewer alice`. The
repo must have Copilot Code Review enabled at the org / repo level.

## Documentation

See <https://abhinav.github.io/git-spice/> for the full upstream
documentation. Gritt additions above this line are not yet covered
there.

## License

This software is distributed under the GPL-3.0 License:

```
git-spice: Stacked Pull Requests
Copyright (C) 2024 Abhinav Gupta

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
```

See LICENSE for details.
