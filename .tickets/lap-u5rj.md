---
id: lap-u5rj
status: open
deps: []
links: []
created: 2026-04-04T10:09:43Z
type: task
priority: 1
assignee: Ronny Unger
tags: [test-gap, fileio]
---
# Add BOM round-trip integration test (read -> edit -> write -> verify BOM preserved)

The test suite has TestReadFile_BOM (BOM detected and stripped from lines) and TestGrepBOMFileHashConsistency (grep and read return same hash for BOM files). However, no test verifies the full cycle: read a BOM file -> edit a line -> write back -> re-read from disk -> confirm BOM bytes still present at byte offset 0.

If WriteFile's BOM-prepend logic (fileio.go:159-162) had a bug, no existing test would catch it.

Spec ref: Phase 2 checklist — 'BOM survived through read/edit/write cycle'.

## Design

Test cases:
1. BOM file 0xEFBBBF + 'line1\nline2\n' -> read -> replace line 2 -> write: on-disk starts with BOM, line 2 replaced, line 1 hash unchanged
2. BOM file -> insert_after line 1 -> write: BOM preserved, new line inserted, line 1 hash unchanged

Component: internal/server/server_test.go

## Acceptance Criteria

1. A test creates a UTF-8 BOM file, reads via lapp_read, edits a line via lapp_edit, then reads raw bytes from disk and confirms:
   - First 3 bytes are 0xEF 0xBB 0xBF
   - Edited content is correct
   - A second lapp_read returns same hashes for unmodified lines

