---
id: lap-1u6m
status: open
deps: []
links: []
created: 2026-04-04T10:08:48Z
type: bug
priority: 0
assignee: Ronny Unger
tags: [correctness, editor]
---
# Return ERR_LINE_OUT_OF_RANGE instead of ERR_HASH_MISMATCH for out-of-bounds refs

When an edit references a line number exceeding file length (e.g. '999#ZZ' in a 50-line file), the spec requires ERR_LINE_OUT_OF_RANGE with recovery guidance 'Agent should re-read the file.' Instead, verifyHashes() at editor.go:248-251 treats this as a hash mismatch (appends RefMismatch with Actual:''), surfacing as ERR_HASH_MISMATCH.

This matters because recovery strategies differ:
- Hash mismatch: error includes remapping table with updated refs — model can retry immediately.
- Line out of range: file has fewer lines than expected — model needs to re-read to understand actual size. Remapping table is meaningless because the referenced line doesn't exist.

The ERR_LINE_OUT_OF_RANGE constant is already defined in types.go:15 but never used.

## Design

Test cases:
1. 5-line file, anchor '10#ZZ' -> ERR_LINE_OUT_OF_RANGE: 'line 10 out of range (file has 5 lines)'
2. 5-line file, range start='3#XX' end='8#YY' -> ERR_LINE_OUT_OF_RANGE for line 8
3. 5-line file, anchor '5#ZZ' (wrong hash) -> ERR_HASH_MISMATCH (line exists, hash wrong)
4. Batch: edit 1 refs line 10 (out of range), edit 2 refs line 3 (wrong hash) -> ERR_LINE_OUT_OF_RANGE takes priority

Spec ref: §6.5 step 1, §8.2. Component: internal/editor/editor.go — verifyHashes()

## Acceptance Criteria

1. Edit referencing line number > len(lines) returns ERR_LINE_OUT_OF_RANGE (not ERR_HASH_MISMATCH).
2. Error message includes the referenced line number and actual file length.
3. If batch contains both out-of-range refs and hash mismatches, ERR_LINE_OUT_OF_RANGE takes priority.
4. The ERR_LINE_OUT_OF_RANGE constant (types.go:15) is used.

