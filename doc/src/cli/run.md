<!-- gs:version unreleased -->

# `gs run`

`gs run` groups commands that execute things locally
(as opposed to interacting with the forge or the git stack).

## `gs run precommit-checks`

Runs the local equivalent of CI checks before pushing.
Useful as a pre-submit guardrail and as a building block
for the agent-driven `gs branch reviews` / `gs branch checks`
workflows that may invoke it via `--fix`.

### Configuration

Checks are loaded in this precedence order:

1. **`<repo-root>/.gitspice/precommit.yaml`** if present.
2. **`<repo-root>/.pre-commit-config.yaml`** if present (delegates to
   `pre-commit run --all-files`; the `pre-commit` framework handles
   per-hook orchestration and reporting).
3. Auto-detected **`mise.toml`** tasks named `lint`, `test`, `build`
   (in that order; whichever exist run as `mise run <name>`).
4. Hardcoded fallback: `mise run lint`, `mise run test`, `mise run build`.

### `.gitspice/precommit.yaml` schema

```yaml
checks:
  - name: lint
    cmd: golangci-lint run
    fail_fast: true
  - name: test
    cmd: go test ./...
    timeout: 5m
  - name: build
    cmd: go build ./...
```

Each entry supports:

- `name` — display name for the check.
- `cmd` — shell command line, executed via `sh -c`.
- `fail_fast` — if true and this check fails, stop the run.
- `timeout` — Go duration string (e.g. `5m`, `30s`); zero means no timeout.

### Flags

- `--only=lint,test` — run a subset of checks by name.
- `--fix` — on failure, hand the captured failure output to Claude
  for diagnosis. Requires the `claude` CLI to be installed and
  configured. The command still exits non-zero on failure even with
  `--fix`; the diagnosis is informational.
