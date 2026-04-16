---
id: lap-1kgr
status: closed
deps: []
links: []
created: 2026-04-04T10:09:02Z
type: bug
priority: 1
assignee: Ronny Unger
tags: [robustness, server]
---
# Cap lapp_grep result size to prevent oversized MCP responses

Originally, handleGrep walked the entire directory tree and accumulated all matches into an unbounded strings.Builder (server.go:322-389). On a large codebase with a broad pattern (e.g. '.' or 'import'), this could produce megabytes of output in a single MCP response, which:

1. Could overflow the model's context window, wasting tokens on truncated content.
2. Could hit MCP transport size limits or cause timeouts.
3. Was inconsistent with lapp_read, which caps output at --limit lines.

The spec's security section (§10) specifically calls out capping response sizes.

Fixed in this PR: handleGrep now caps at 100 file matches, maxOutputLines for text mode, and maxStructuredMatches (500) for structured mode, with truncation signaling.

## Design

Test cases:
1. 200 files each matching, cap=100 files -> first 100 files + truncation note
2. 5 files matching, all within cap -> full results, no truncation note
3. 5000 lines across 10 files, line cap=1000 -> truncation after 1000 output lines

Spec ref: §5.5, §10. Component: internal/server/server.go — handleGrep()

## Acceptance Criteria

1. handleGrep caps output at a configurable maximum (e.g. 100 file matches or 1000 output lines, whichever first).
2. When truncated, response includes: '[Results truncated. N files matched; showing first M. Narrow your pattern or specify a path.]'
3. Files walked in filepath.WalkDir order (deterministic, alphabetical within a directory).

