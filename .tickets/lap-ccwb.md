---
id: lap-ccwb
status: closed
deps: []
links: []
created: 2026-04-04T10:08:14Z
type: bug
priority: 0
assignee: Ronny Unger
tags: [correctness, editor]
---
# Guard NormalizeNewlines against destroying literal backslash-n in code content

NormalizeNewlines unconditionally replaces every literal two-character backslash-n sequence with a real newline. Intended to fix a common model error (sending \\n in JSON instead of \n). But if the model correctly sends content containing legitimate literal backslash-n (regex patterns, format strings, escape sequences), those are destroyed. Example: content 'fmt.Println("hello\nworld")' gets its backslash-n replaced with a real newline, producing broken code. Root cause: editor.go:102-108 does not distinguish 'all newlines were accidentally escaped' from 'content legitimately contains backslash-n alongside real newlines'. The fix: only normalize when content has zero real newlines but one or more literal backslash-n sequences.

## Design

Test cases:
1. 'line1\nline2' (no real newlines) -> Normalize to two lines
2. 'line1' + newline + 'line2' -> No change
3. 'fmt.Println("hello\nworld")' + newline + 'return nil' -> No change (backslash-n is intentional code)
4. 'a\nb\nc' (no real newlines) -> Normalize to three lines
5. Empty string -> No change

Spec ref: §6.3. Component: internal/editor/editor.go — NormalizeNewlines()

## Acceptance Criteria

1. If content contains zero real newlines but one or more literal backslash-n sequences, normalize all to real newlines.
2. If content already contains at least one real newline, do not replace literal backslash-n sequences.
3. Logging still fires when normalization is applied.

