---
id: lap-8csq
status: closed
deps: []
links: []
created: 2026-04-04T10:10:09Z
type: chore
priority: 2
assignee: Ronny Unger
tags: [documentation, editor]
---
# Document SelfCorrectResult.Note always-set behavior as known spec deviation

The spec §5.1 describes a two-phase self-correcting flow:
1. First failure: SelfCorrectResult with Note absent.
2. Second consecutive failure on same file: SelfCorrectResult with Note set to escalation hint.

The implementation at editor.go:383 always sets Note because MCP is stateless — no way to track consecutive failures across calls. The code comment references 'pre-mortem fix pm-20260404-004' but this deviation is not documented anywhere a reviewer or spec reader would find it.

Legitimate architectural decision, but undocumented outside a code comment.

## Design

N/A — documentation change only. Existing TestRoundTrip_SelfCorrect_NoteAlwaysSet already validates the behavior.

Spec ref: §5.1. Components: spec, types.go doc comment

## Acceptance Criteria

1. The spec (or companion decisions document) explicitly records this deviation: what the spec says (two-phase), what the implementation does (always includes Note), why (MCP is stateless), and impact (slightly more aggressive escalation — acceptable trade-off).
2. The SelfCorrectResult.Note field's doc comment in types.go:68 references the deviation.

