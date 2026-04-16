---
id: lap-v8i8
status: open
deps: []
links: []
created: 2026-04-16T10:00:00Z
type: task
priority: 2
assignee: Ronny Unger
tags: [tech-debt, linting]
---
# Lower gocognit and cyclop thresholds after refactoring

.golangci.yml has temporarily elevated complexity thresholds:

- gocognit: 120 (target: 30) — blocked by `handleGrep` and `generateDiff`
- cyclop: 60 (target: 22) — blocked by `handleGrep` and `handleInsertBlock`

Once these functions are refactored into smaller units, lower the
thresholds to their target values and verify no new findings.

## Acceptance Criteria

1. gocognit min-complexity lowered to 30.
2. cyclop max-complexity lowered to 22.
3. `golangci-lint run` passes with zero issues at new thresholds.