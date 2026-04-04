---
id: lap-srw6
status: closed
deps: []
links: []
created: 2026-04-04T10:11:05Z
type: task
priority: 2
assignee: Ronny Unger
tags: [robustness, fileio, windows]
---
# Surface actionable error message for Windows sharing violation on rename

The spec §9.1 explicitly requires that when os.Rename fails on Windows because another process has the file open (common with IDEs, linters, antivirus), the error message should be actionable:

  'Cannot write: another process has the file open. Close it in your editor and retry.'

Currently WriteFile at fileio.go:201 returns generic ErrWriteFailed on rename failure. On Unix this is rarely hit (rename is atomic even with open file handles), but on Windows MoveFileExW fails with ERROR_SHARING_VIOLATION when another process holds the file. The generic error gives the user no guidance.

Additionally, handleWrite at server.go:272 returns 'cannot rename temp file: ' + err.Error(), which includes raw OS error but not the user-friendly guidance.

## Design

Test cases:
1. (Windows) File held open by another process -> rename fails -> error contains 'another process has the file open'
2. (Unix) Normal rename failure -> generic ERR_WRITE_FAILED (unchanged)

Note: Difficult to test in CI without Windows runner. Consider build-tagged test file (writefix_windows_test.go).

Spec ref: §9.1. Components: internal/fileio/fileio.go — WriteFile(), potentially a new writefix_windows.go

## Acceptance Criteria

1. On Windows, if os.Rename fails with sharing violation, return: 'ERR_WRITE_FAILED: Cannot write: another process has the file open. Close it in your editor and retry.'
2. On non-Windows platforms, behavior unchanged (generic ErrWriteFailed).
3. Check uses errors.Is or OS-specific error codes, not string matching.

