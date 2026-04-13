---
id: lap-ija8
status: closed
deps: [lap-818l]
links: []
created: 2026-04-13T11:00:56Z
type: task
priority: 2
assignee: Ronny Unger
parent: lap-j6au
tags: [benchmark, instructions]
---
# Sync prompts and instructions after stale repair

Update benchmark prompts/instructions only if the stale-ref payload changes how models should retry.

## Acceptance Criteria

Prompt syntax passes and instruction file mentions local stale retry payloads if needed.

