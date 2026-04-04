---
id: lap-jnt8
status: closed
deps: []
links: []
created: 2026-04-04T10:10:19Z
type: chore
priority: 2
assignee: Ronny Unger
tags: [cleanup, editor]
---
# Remove unused ReadRequest and WriteRequest types

ReadRequest (types.go:73-77) and WriteRequest (types.go:79-82) are defined in the data contracts (spec §11) but never referenced anywhere in the codebase. The server handlers extract parameters directly from MCP's CallToolRequest.GetArguments().

These types add noise for reviewers who expect them to be wired up. If intended for future use (e.g. typed request parsing), that should be tracked separately. Otherwise remove.

## Design

Test cases:
1. grep -rn 'ReadRequest|WriteRequest' --include='*.go' after removal -> no matches
2. go build ./... -> success

Spec ref: §11. Component: internal/editor/types.go

## Acceptance Criteria

1. ReadRequest and WriteRequest removed from types.go.
2. go build ./... and go test ./... still pass.
3. No other file references these types (verify with grep -r 'ReadRequest\|WriteRequest').

