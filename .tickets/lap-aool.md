---
id: lap-aool
status: closed
deps: []
links: []
created: 2026-04-13T13:53:43Z
type: epic
priority: 1
assignee: Ronny Unger
tags: [lapp, benchmark, v2]
---
# Implement lapp Benchmark V2

Add a V2 benchmark family with suite-file loading, strategy metadata, new suites, and reporting by file-size/change-count/strategy.

## Acceptance Criteria

V2 suites exist, can be run independently of V1, and at least one V2 suite has been run across 3+ models.


## Notes

**2026-04-13T14:07:16Z**

2026-04-13 narrowed per pre-mortem: Milestone 1 now implements exactly one suite (v2-bulk-edits-same-file) with one exact case (mwaskom__seaborn-3069) and four strategies only: native-edit, lapp-text-grep, lapp-structured-grep, lapp-replace-block. bottom-up/script-generation and other V2 suites deferred.
