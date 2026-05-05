<!-- gs:version unreleased -->

# `gs branch checks`

Walk failing CI checks for the current PR.

Fetches failing CI check runs for the current branch's pull request,
classifies them with Claude AI, and walks through them one by one.

For each check, you choose to:

- **address with agent** — spawn a focused Claude session with the
  check's failure log; the agent makes commits to fix the failure.
- **skip** — no-op.
- **defer** — record for later.

Use `--batch` to address all failing checks in a single Claude session.

Unlike `gs branch reviews`, there is no reply-posting step — checks
have no thread. After the agent commits, push the branch (or wait for
CI to retry on the existing branch) to verify the fix.

## Check log fetching

`gs` tries to fetch the failing check's log to give Claude full
context. If the forge does not expose a log API (for example, GitHub's
`GET /repos/.../check-runs/.../logs` requires HTTP plumbing not yet
wired through `gs`), the agent receives a summary instead:

```
Check "lint" ended with conclusion "failure" (status: completed).
Log unavailable: <error>.
View details at: https://github.com/.../runs/12345
```

The walk continues; one missing log does not abort the run.

## Flags

- `--branch=NAME` — operate on a specific branch (defaults to current).
- `--batch` — run all items in one Claude session.
- `--include-passing` — include passing/in-progress checks too.
- `--concurrency=N` — parallel classifications (default 4).
