---
id: lap-y20r
status: closed
deps: []
links: []
created: 2026-04-04T10:10:29Z
type: chore
priority: 2
assignee: Ronny Unger
tags: [documentation, server]
---
# Include lapp_grep in CLAUDE.md recommendation and stderr hint

The recommended CLAUDE.md entry and stderr startup hint both say:

  Prefer lapp_read / lapp_edit / lapp_write over the built-in Read / Edit / Write tools.

This omits lapp_grep, a key tool enabling 2-call search-and-edit workflows (lapp_grep -> lapp_edit) instead of 3-call (grep -> lapp_read -> lapp_edit). Without mentioning it, the model may use the built-in Grep and then fail when trying to use those results with lapp_edit.

## Design

N/A — documentation/string change only.

Spec ref: §5.1 mitigation #4. Components: README.md, internal/server/server.go

## Acceptance Criteria

1. README CLAUDE.md recommendation mentions all four tools: lapp_read / lapp_edit / lapp_write / lapp_grep.
2. stderr hint at server.go:29 mentions all four tools.
3. The hint also mentions the built-in tool it replaces: Grep.

