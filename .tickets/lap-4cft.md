---
id: lap-4cft
status: closed
deps: []
links: []
created: 2026-04-13T13:53:43Z
type: task
priority: 1
assignee: Ronny Unger
parent: lap-aool
tags: [benchmark, runner]
---
# Add runner support for V2 suite files and metadata

Extend benchmark/run.py to load suite files beyond V1 and store strategy/file-size/change-count metadata.

## Acceptance Criteria

Runner supports --suite-file and V2 metadata is present in result JSON.


## Notes

**2026-04-13T14:07:16Z**

Acceptance narrowed: runner metadata only needs to support the four Milestone 1 strategies and the concrete bulk-edit suite.
