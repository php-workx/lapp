# Lapp Tool Selection Policy

This instruction is designed to be always loaded so agents reliably choose
lapp tools for file reading and editing when they are available.

Keep this file in the `instructions` array in your OpenCode config — not in a
skill — so it is active without an extra load step.

## Why lapp

lapp reads and edits files using LINE#HASH references. Every line is tagged
with its line number and a 2-character content hash. Edits reference those
tags rather than reproducing surrounding code. This means:

- Edits fail loudly if the file changed since the read (no silent wrong-line edits)
- Multi-line replacements only require the anchor hash, not the full old content
- Batch edits to the same file are validated atomically — all succeed or none apply

## Tool Selection (Critical)

Use the right tool for the task. `lapp_edit` is preferred whenever lapp tools
are available, but the read path depends on file size.

| Task | First tool | Why |
|---|---|---|
| Targeted change in any file | `lapp_grep → lapp_edit` | grep returns LINE#HASH directly; no full read needed |
| Read small file (<300 lines) for context | `lapp_read` | Gets all references in one call |
| Read large file (300+ lines) for context | `lapp_read` with offset/limit | Paginate; grep is usually faster for targeted edits |
| Multiple scattered edits in one file | `lapp_read → lapp_edit` (batch) | All edits in a single atomic call |
| Create a new file | `lapp_write` | For new files only; errors if file already exists |
| Search before editing | `lapp_grep` | Returns LINE#HASH refs usable directly in `lapp_edit` |

## Standard Workflows

**Targeted edit (preferred — 2 calls):**
1. `lapp_grep "<search pattern>" path=<file>` → get the LINE#HASH for the target line
2. `lapp_edit` with that anchor and the new content → done

**Full-context edit (small files or multiple changes):**
1. `lapp_read` → see all lines with hashes
2. `lapp_edit` with all edits batched in one call → atomic

**Hash mismatch recovery:**
If `lapp_edit` returns a hash mismatch error, the response includes an updated
LINE#HASH remapping table. Use those updated references and retry immediately —
do not re-read the whole file.

## When NOT to Use lapp

- lapp tools are not available in the tool manifest → fall back to native read/edit
- The file does not exist yet → `lapp_write` creates it; `lapp_edit` will error
- A binary file → lapp rejects binaries with a clear error; use native tools
- The agent is readonly and cannot call editing tools

## Fallback Policy

- If `lapp_edit` fails with hash mismatch → use the remapping table in the error and retry
- If `lapp_grep` returns no matches → try `lapp_read` to get full context
- If lapp tools are not configured → fall back to native `read` + `edit`
- If `lapp_write` returns ERR_FILE_EXISTS → use `lapp_read` + `lapp_edit` instead

## Anti-Patterns

- Do NOT use native `read`/`edit` when `lapp_read`/`lapp_edit` are available
- Do NOT skip `lapp_read` or `lapp_grep` before `lapp_edit` — hash refs are required
- Do NOT use `lapp_write` to overwrite an existing file (use `lapp_edit`)
- Do NOT treat a hash mismatch as a hard failure — it is a self-correcting signal
- Do NOT use `lapp_grep` for exact line-number lookups — use `lapp_read` with offset
