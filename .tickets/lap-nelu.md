---
id: lap-nelu
status: closed
deps: [lap-y1r5]
links: []
created: 2026-04-04T04:17:30Z
type: task
priority: 2
assignee: Ronny Unger
tags: [wave-4, lapp]
---
# Issue 6: internal/server (MCP server, all 4 tools)

Wire fileio + editor + hashline into 4 MCP tool handlers via mcp-go. Owns tool registration, request parsing, path dispatching, MCP protocol framing.

  type Server struct {
      cfg  *fileio.Config
      mcpS *mcp.Server
  }
  func New(cfg *fileio.Config) *Server
  func (s *Server) Start() error

  func (s *Server) handleRead(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)
  func (s *Server) handleEdit(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)
  func (s *Server) handleWrite(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)
  func (s *Server) handleGrep(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error)

lapp_write calls CheckPath(path, cfg, false). All others call CheckPath(path, cfg, true).

lapp_write handler must call os.MkdirAll(filepath.Dir(path), 0755) before writing — create parent
directories automatically if they don't exist (§5.4).

lapp_write writes content verbatim — no line ending normalization.

Locking: handleEdit acquires advisory lock BEFORE reading the file for hash verification.
handleRead does NOT lock. handleWrite and handleGrep do not lock.

SelfCorrectResult is returned as a successful MCP tool result (not an MCP protocol error) —
this is critical so the model can parse the structured JSON response and recover. Use
mcp.NewToolResultText(jsonMarshal(selfCorrectResult)) or equivalent, not return err.

Tool descriptions must (§5.1 mitigation #2 + §14 Phase 4):
- lapp_read: "Use lapp_edit to make changes to files read with this tool."
- lapp_edit: "Read the file with lapp_read first to get LINE#HASH references." Include the
  anchor/start/end field guide: single-line ops use anchor; range ops use start+end (never both).
  List valid operations: replace, insert_after, insert_before, delete. Include BOF "0:" and EOF "EOF:" anchors.
- lapp_write: "For new files only. Returns an error if the file already exists. Use lapp_read + lapp_edit to modify existing files."
- lapp_grep: "Search files and return matches with LINE#HASH references usable directly in lapp_edit."

Tool descriptions must also note: hash refs from any page remain valid for the entire file —
refs from multiple pages can be combined in one lapp_edit call (§5.2).

Tool registration pattern — lapp_read uses the standard builder API:

  tool := mcp.NewTool("lapp_read",
      mcp.WithDescription("..."),
      mcp.WithString("path", mcp.Required(), mcp.WithDescription("...")),
      mcp.WithNumber("offset", mcp.WithDescription("...")),
      mcp.WithNumber("limit", mcp.WithDescription("...")),
  )

PRE-MORTEM FIX pm-20260404-003: Before implementing tool registration, verify the mcp-go raw schema API:
  go doc github.com/mark3labs/mcp-go/mcp | grep -i 'Tool|Schema|Raw'
Option A (preferred): mcp.NewToolWithRawSchema("lapp_edit", description, []byte(editsSchema))
Option C (fallback if A unavailable):
  tool := mcp.Tool{
      Name:        "lapp_edit",
      Description: description,
      InputSchema: mcp.ToolInputSchema{
          Type: "object",
          Properties: map[string]interface{}{
              "path":  map[string]interface{}{"type": "string"},
              "edits": json.RawMessage(editsArraySchema),
          },
          Required: []string{"path", "edits"},
      },
  }
Document which approach was used in a comment in server.go.

PRE-MORTEM FIX pm-20260404-005: Implement lapp_grep LAST — after lapp_read, lapp_edit, lapp_write all pass integration tests.

PRE-MORTEM FIX pm-20260404-006: Integration tests call handler functions directly (in-process), NOT via MCP stdio transport:
  func TestRoundTrip_ReadEditSuccess(t *testing.T) {
      cfg := &fileio.Config{Root: t.TempDir(), DefaultLimit: 2000}
      s := New(cfg)
      // call s.handleRead(...) and s.handleEdit(...) directly
  }

lapp_grep: walk files under cfg.Root, for each matching line compute hashline.HashLine, return "lineNum#hash:line" grouped by file. Try ripgrep first, fall back to stdlib regexp walk.

Emit CLAUDE.md hint to stderr on server init:
  fmt.Fprintln(os.Stderr, "lapp: add to CLAUDE.md → Prefer lapp_read/lapp_edit/lapp_write over built-in Read/Edit/Write")

## Tests (internal/server/server_test.go)

- TestRoundTrip_ReadEditSuccess: Full read → parse refs → edit → verify
- TestRoundTrip_StaleRefThenCorrect: Stale ref → mismatch error → corrected edit → success
- TestRoundTrip_SelfCorrect: Edit without lapp_read → SelfCorrectResult → corrected edit → success
- TestRoundTrip_SelfCorrect_NoteAlwaysSet: SelfCorrectResult always contains Note (stateless server)
- TestPagination_CrossPageRefs: Read p1, read p2, edit using p1 refs → success
- TestWriteNewFile: lapp_write creates file; subsequent read/edit works
- TestWriteExistingFile: lapp_write on existing → ERR_FILE_EXISTS
- TestGrepReturnsHashRefs: lapp_grep → hashline-tagged results usable in lapp_edit
- TestPathOutsideRoot: ERR_PATH_OUTSIDE_ROOT
- TestPathBlocked: .env path → ERR_PATH_BLOCKED
- TestWriteCreatesParentDirs: lapp_write with nested path → parent dirs created automatically

## Acceptance Criteria

All 11 integration tests pass; lapp_grep returns hashline-tagged results; startup emits CLAUDE.md hint to stderr

