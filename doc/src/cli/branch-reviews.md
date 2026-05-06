<!-- gs:version unreleased -->

# `gs branch reviews`

Walk through PR review threads with Claude AI.

Fetches open review threads for the current branch's pull request,
classifies them with Claude AI, and walks through them one by one.
For each thread, you choose to:

- **address with agent** — spawn a focused Claude Code session that
  reads the comment, makes the code change, runs tests, commits with
  an `Addresses #<id>` marker, and exits. `gs` then posts an
  `Addressed in <sha>: <subject>` reply on the thread.
- **reply only** — post a manual reply.
- **edit reply** — open `$EDITOR` on the suggested reply, then post.
- **skip** — no-op this run; the thread reappears next time.
- **defer** — record the thread for later (persisted across runs in
  `.git/spice/address-deferred`; auto-cleaned when the thread receives
  new activity or is resolved remotely).

Use `--batch` to address all threads in a single Claude session. The
agent is given a consolidated `INSTRUCTIONS.md` and is expected to
make one commit per item with the `Addresses #<id>` marker. `gs` then
posts replies for each item the agent committed.

## Bot filtering

By default, comments from these bot logins are included:

- `copilot[bot]`
- `claude[bot]`
- `codex[bot]`
- `github-advanced-security[bot]`

Override via `--bot-allowlist=name1,name2`. Empty allowlist excludes
all bots. The `[bot]` suffix is stripped during comparison.

## Reply format

Replies posted by `gs` have the form:

```
Addressed in 1a2b3c4: <commit subject>
```

Both the SHA and the subject are included so the reply remains
informative even after the SHA link 404s due to a rebase or squash —
common in stacked PR workflows.

## Branch-discipline warning

If a fix would touch a file whose most recent commit lives on a base
branch in the current stack, `gs` prints a warning to stderr before
the TUI launches:

```
⚠ item N: file FILE was last modified on base branch "BASE" in this stack;
  fixing here will apply only to the current PR's diff, not the base.
```

It also runs a best-effort `git merge-tree` pre-flight to predict
which upper branches would conflict if the fix were applied to the
base. Warnings are informational; the user can Ctrl-C to abort.

## Flags

- `--branch=NAME` — operate on a specific branch (defaults to current).
- `--batch` — run all items in one Claude session.
- `--include-resolved` — include resolved threads (default: open only).
- `--bot-allowlist=copilot,claude,...` — bots to include.
- `--reset-deferred` — clear `.git/spice/address-deferred` before running.
- `--concurrency=N` — parallel classifications (default 4).

## Skip already-addressed threads

If `gs` posted an `Addressed in <sha>` reply on a thread (and no one
has replied since), the thread is skipped automatically — no need to
manually filter it out. The check is implicit (no local state file);
the GitHub thread itself is the source of truth.
