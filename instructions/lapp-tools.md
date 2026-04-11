# Lapp File Editing Policy

Load via `instructions` array — always active, no skill load needed.

## Tool selection

| Task | Use |
|---|---|
| Targeted edit, any size file | `lapp_grep` → `lapp_edit` |
| Need full context / small file (<300 lines) | `lapp_read` → `lapp_edit` |
| New file | `lapp_write` |
| Batch edits to one file | `lapp_read` → single `lapp_edit` call |
| Search term contains code or special chars | `lapp_grep` with `literal=true` |

## Workflows

**Fast (preferred):** `lapp_grep "<pattern>" path=<file>` → get LINE#HASH → `lapp_edit`

**Code search (special chars in search term):** `lapp_grep "<pattern>" literal=true` — use when the search term contains `\`, `(`, `)`, `?`, `+`, `*`, `.`, `[`, `]`, `|`, `^`, `$`

**Full-read:** `lapp_read` → pick refs → `lapp_edit` (batch all edits in one call)

**Hash mismatch:** use the remapping table in the error, retry immediately — do not re-read.

## Rules

- Prefer lapp tools over native read/edit whenever available
- Never call `lapp_edit` without first getting LINE#HASH refs from grep or read
- Never use `lapp_write` on an existing file (use `lapp_edit`)
- Fall back to native tools only if lapp is not in the tool manifest
