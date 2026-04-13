---
id: lap-g1v6
status: open
deps: [lap-jwqr]
links: []
created: 2026-04-13T11:00:56Z
type: task
priority: 2
assignee: Ronny Unger
parent: lap-j6au
tags: [validation]
---
# Run final validation after stale repair

Run go test, python syntax checks, and benchmark report rendering after stale-ref payload implementation.

## Acceptance Criteria

go test ./..., py_compile, and benchmark/v1_report.py all pass.

