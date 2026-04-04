# lapp Hashline MCP Server — Architecture

## Purpose
MCP server giving AI coding agents hashline-addressed file editing.
Every line tagged LINE#HASH:content on read; model references those tags to edit.
Hash mismatch rejects entire batch. Matches oh-my-pi's algorithm exactly.

## Module
`github.com/lapp-dev/lapp` — Go 1.24

## Package Structure
- `pkg/hashline/` — exported core: HashLine, FormatLine, ParseRef, VerifyRef
  - Algorithm: strip \r, trimRight whitespace, seed=lineNum if non-alphanumeric else 0, xxHash32 & 0xFF, encode via ZPMQVRWSNKTXJBYH
  - Uses github.com/OneOfOne/xxhash v1.2.8 (NOT v1.2.9 which doesn't exist)
- `internal/editor/` — types.go (all 15 error constants + data types), editor.go (edit engine)
  - ApplyEdits: validate → detect overlaps → sort bottom-up → apply splices → no-op check → write
  - Same-anchor inserts: sort DESCENDING by idx so final result preserves array order
  - SELF_CORRECT path when no valid hashline refs found
- `internal/fileio/` — fileio.go, lock_unix.go (!windows), lock_windows.go
  - CheckPath(path, cfg, mustExist bool) — EvalSymlinks on parent when mustExist=false (new files)
  - Atomic write: temp file + chmod + rename
  - Lock files in os.UserCacheDir()/lapp/locks/
- `internal/server/` — MCP server wiring all 4 tools via mcp-go
  - mcp.NewToolWithRawSchema for lapp_edit (enum in nested array)
  - SelfCorrectResult returned as JSON text result, NOT MCP error
  - Lock acquired in handleEdit before reading for hash verification
- `cmd/lapp/` — CLI with --root, --limit, --block, --allow, --log-file, --version
  - buildVersion set via goreleaser ldflags

## Test Coverage
- 9 hashline tests (all 21 oh-my-pi-compatible vectors pass)
- 14 fileio tests
- 26 editor tests
- 11 server integration tests

## Key Implementation Notes
- macOS t.TempDir() → /var/... symlink; must EvalSymlinks on cfg.Root in tests
- mcp-go: use mcp.Description() (PropertyOption) not mcp.WithDescription() inside WithString/WithNumber
- go.mod uses xxhash v1.2.8 (corrected from spec's v1.2.9 which doesn't exist)
