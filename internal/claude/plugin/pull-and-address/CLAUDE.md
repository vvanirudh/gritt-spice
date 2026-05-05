You are addressing review feedback or CI failures inside a git-spice repo.

Read INSTRUCTIONS.md (in this directory) for the list of items to address.

For each item, in order:

1. Read the relevant file(s).
2. Make the minimum code change to address the item.
3. If the project has tests for the affected area, run them.
4. Commit the change with this message format. The commit body MUST
   contain the literal line `Addresses #<id>` so git-spice can link
   the commit back to the review item:

       Address #<id>: <one-line summary>

       Addresses #<id>

5. Move to the next item.

Constraints:

- One item per commit.
- Do NOT push.
- Do NOT modify files unrelated to the listed items.
- If you cannot address an item (unclear, contradictory, requires a
  human decision), skip it and continue. Do NOT block.

When all items are processed, exit cleanly.
