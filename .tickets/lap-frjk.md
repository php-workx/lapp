---
id: lap-frjk
status: closed
deps: []
links: []
created: 2026-04-04T10:09:28Z
type: bug
priority: 1
assignee: Ronny Unger
tags: [robustness, server]
---
# Set sensible default permissions on files created by lapp_write

Previously, handleWrite created the temp file with permissions 0600 (server.go:259) and renamed it without calling os.Chmod. The resulting file was owner-readable/writable only, overly restrictive for source code files (conventionally 0644).

Consequences:
- Other processes (linters, build tools, IDE indexers) checking group/other bits may fail to read.
- File appears with unusual permissions in ls -la, surprising the user.
- On shared-user systems, collaborators cannot read the file.

Root cause: Unlike fileio.WriteFile (which reads info.Mode() from the existing file), handleWrite had no existing file to reference and never set a default.

Fixed in this PR: server.go now calls os.Chmod(tmpPath, 0644) before rename.

## Design

Test cases:
1. lapp_write creates new file -> permissions are 0644
2. lapp_write to nested path -> file is 0644, parent dirs are 0755 (already correct)

Spec ref: §9.1 steps 3-4. Component: internal/server/server.go — handleWrite()

## Acceptance Criteria

1. Files created by lapp_write have permissions 0644 (owner rw, group/other read).
2. Temp file still created with 0600 for security during write, then os.Chmod(tmpPath, 0644) called before rename.

