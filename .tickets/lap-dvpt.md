---
id: lap-dvpt
status: open
deps: []
links: []
created: 2026-04-04T10:10:51Z
type: task
priority: 2
assignee: Ronny Unger
tags: [test-gap, server]
---
# Add integration test for concurrent edits serialized by file lock

TestLock_Serializes tests that the lock mechanism itself works (second goroutine gets ErrLocked). However, no test exercises the full handleEdit path with two concurrent edit requests on the same file to verify that:

1. The first edit succeeds.
2. The second edit gets ERR_LOCKED (or succeeds after first completes, depending on timing).
3. The file is not corrupted.

This matters because locking happens inside handleEdit (server.go:189-193), and the integration between lock acquisition, file read, hash verification, and atomic write needs end-to-end validation.

Spec ref: Phase 2 — 'Concurrent writes to same file serialized by lock'.

## Design

Test cases:
1. Two goroutines each replace line 1 of same file -> one succeeds, one gets ERR_LOCKED
2. After both complete, file on disk contains exactly one replacement -> no corruption
3. After edit completes, no .lock files in project root tree

Component: internal/server/server_test.go

## Acceptance Criteria

1. A test launches two goroutines, each calling handleEdit on the same file simultaneously.
2. Exactly one edit succeeds; the other either gets ERR_LOCKED or succeeds after first's lock is released.
3. File content on disk is consistent (no partial writes, no corruption).
4. No .lock files exist inside the project root directory tree after completion (lock files live in os.UserCacheDir/lapp/locks/ only — related to finding P2 #11).

