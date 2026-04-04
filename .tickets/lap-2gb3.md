---
id: lap-2gb3
status: open
deps: []
links: []
created: 2026-04-04T10:10:38Z
type: task
priority: 2
assignee: Ronny Unger
tags: [test-gap, cli]
---
# Add test for --version flag

The --version flag (main.go:48-51) prints buildVersion and exits, but no test covers this. Since main() calls os.Exit, a direct unit test is awkward, but a subprocess test (via exec.Command) can verify the flag works and the exit code is 0.

Spec ref: Phase 4 — '--version flag prints version and exits'.

## Design

Test cases:
1. lapp --version -> stdout contains version string (e.g. 'dev'), exit code 0
2. lapp --version does not start the MCP server -> process exits immediately (no stdin blocking)

Component: cmd/lapp/main.go

## Acceptance Criteria

1. A test runs lapp --version as a subprocess.
2. Stdout contains the version string (at minimum, non-empty output).
3. Exit code is 0.

