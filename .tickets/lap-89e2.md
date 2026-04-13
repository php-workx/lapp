---
id: lap-89e2
status: closed
deps: []
links: []
created: 2026-04-13T10:50:44Z
type: task
priority: 1
assignee: Ronny Unger
parent: lap-j6au
tags: [lapp, multiline]
---
# Implement block indentation rebasing

Rebase new multiline replacement blocks onto the old block's shared indentation in lapp_replace_block.

## Acceptance Criteria

Server tests pass and multiline helper preserves base + relative indentation.

