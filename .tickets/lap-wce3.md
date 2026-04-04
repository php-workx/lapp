---
id: lap-wce3
status: closed
deps: []
links: []
created: 2026-04-04T10:09:13Z
type: bug
priority: 1
assignee: Ronny Unger
tags: [build, distribution]
---
# Lower go.mod minimum version from 1.25.0 to 1.24

go.mod declares 'go 1.25.0' but the spec (§2) requires only Go 1.24+. Users on Go 1.24.x (still actively supported) get a build error:

  go: module requires Go >= 1.25.0 (running go 1.24.x)

This needlessly excludes 1.24.x users from the primary distribution method (go install) documented in the README. Since Go 1.24 is still within its support window, the minimum should match the spec unless a 1.25-only feature is actually used. No Go 1.25-specific language features appear in the codebase.

Root cause: go.mod line 3 — likely set by the local toolchain during go mod init or go mod tidy.

## Design

Test cases:
1. go build ./... with Go 1.24.x toolchain -> builds successfully
2. go vet ./... with Go 1.24.x toolchain -> no errors

Spec ref: §2. Component: go.mod

## Acceptance Criteria

1. go.mod declares 'go 1.24' (or 'go 1.24.0').
2. go build ./... succeeds on Go 1.24.x.
3. No Go 1.25-only language features are used in the codebase.

