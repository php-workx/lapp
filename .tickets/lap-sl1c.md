---
id: lap-sl1c
status: closed
deps: []
links: []
created: 2026-04-04T10:08:29Z
type: bug
priority: 0
assignee: Ronny Unger
tags: [correctness, editor]
---
# Reject BOF/EOF special anchors on insert_before

Spec §6.1 states '0:' (BOF) and 'EOF:' are valid for insert_after only. However, validateOne() handles EditInsertAfter and EditInsertBefore in the same case branch (editor.go:168) and does not check for special anchors when type is insert_before. The guard exists in parseAddressing() (line 212-214: 'BOF/EOF anchors only valid for insert_after'), but inserts bypass parseAddressing entirely — they parse the anchor directly at line 178.

Consequences:
- insert_before with '0:': lineNum=0 -> startLine=0 -> applyOne computes pos=0-1=-1, clamped to 0. Accidentally inserts at beginning, same as insert_after with '0:'. Confusing but not data-corrupting.
- insert_before with 'EOF:': lineNum=-1 -> lineNum=len(lines) -> startLine=len(lines) -> applyOne computes pos=len(lines)-1. Inserts BEFORE the last line, not at the end. Silently wrong — content lands one line too early.

Fix: after ParseRef returns at line 178, if lineNum==0 || lineNum==-1 and e.Type==EditInsertBefore, return ERR_INVALID_EDIT.

## Design

Test cases:
1. insert_before + '0:' -> ERR_INVALID_EDIT
2. insert_before + 'EOF:' -> ERR_INVALID_EDIT
3. insert_after + '0:' -> Success (prepends)
4. insert_after + 'EOF:' -> Success (appends)

Spec ref: §6.1, §6.2. Component: internal/editor/editor.go — validateOne()

## Acceptance Criteria

1. insert_before with anchor '0:' returns ERR_INVALID_EDIT with message 'BOF/EOF anchors only valid for insert_after'.
2. insert_before with anchor 'EOF:' returns ERR_INVALID_EDIT with the same guidance.
3. insert_after with '0:' and 'EOF:' continues to work as before.

