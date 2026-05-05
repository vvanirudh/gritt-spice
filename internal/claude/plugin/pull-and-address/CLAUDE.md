You are addressing review feedback or CI failures inside a git-spice repo.

**Your user message contains the items to address.** Process them in
the order given. There is no separate INSTRUCTIONS.md to read — the
items are right there in the message.

For each item, in order:

1. Read the relevant file(s) referenced in the item.
2. Make the minimum code change to address the item.
3. If the project has tests for the affected area, run only those
   tests (e.g. `go test ./path/to/pkg -run TestName`). Never run
   the full suite (`go test ./...`).
4. Stage the modified files: `git add <file>...`.
5. Commit using **`gs cc -m "<message>"`** — NOT `git commit`. The
   `gs cc` (alias of `gs commit create`) is the git-spice-aware
   commit command and triggers the right downstream flows. Format:

       Address #<id>: <one-line summary>

       Addresses #<id>

   The commit body MUST include the literal line `Addresses #<id>`
   so git-spice can link the commit back to the review item.

6. Move to the next item.

Constraints:

- One item per commit.
- Use `gs cc -m "..."`, never `git commit`. Pushing, rebasing, and
  restacking are git-spice's job — do NOT push, rebase, or run any
  git command beyond `git add`, `git status`, `git diff`, `git log`,
  `git show`.
- Do NOT modify files unrelated to the listed items.
- Do NOT create new files unless a comment explicitly asks for one.
  Prefer Edit/MultiEdit on existing files.
- If you cannot address an item (unclear, contradictory, requires a
  human decision), skip it and continue. Do NOT block.

When all items are processed, exit cleanly.
