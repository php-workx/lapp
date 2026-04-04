---
id: lap-8xgv
status: closed
deps: [lap-c0wk, lap-lsuv]
links: []
created: 2026-04-04T04:16:37Z
type: task
priority: 2
assignee: Ronny Unger
tags: [wave-2, lapp]
---
# Issue 3: pkg/hashline + internal/editor/types.go

pkg/hashline is the exported core of lapp — importable by agent builders without the MCP layer. Owns hash algorithm, line formatting, and reference parsing. internal/editor/types.go owns data contracts shared between editor and server layers.

pkg/hashline/hashline.go:

  const Alphabet = "ZPMQVRWSNKTXJBYH"

  // HashLine computes the 2-character hash for a line.
  // lineNum is 1-indexed. BOM bytes must be stripped before calling.
  // 1. Strip all \r
  // 2. TrimRight " \t\n"
  // 3. Significance check: seed = 0 if any unicode.IsLetter||unicode.IsDigit, else lineNum
  // 4. xxhash.Checksum32S([]byte(processed), uint32(seed))  ← NOT New32()
  // 5. b := hash & 0xFF
  // 6. return string([]byte{Alphabet[b>>4], Alphabet[b&0x0F]})
  func HashLine(line string, lineNum int) string

  // FormatLine returns "LINE#HASH:CONTENT"
  func FormatLine(line string, lineNum int) string

  // ParseRef: "N#XX" → (N, "XX", nil) | "0:" → (0, "", nil) BOF | "EOF:" → (-1, "", nil) EOF | other → error
  func ParseRef(ref string) (lineNum int, hash string, err error)

  func VerifyRef(ref string, lines []string) error

internal/editor/types.go — all 15 error code string constants plus all types:

  type EditType string
  const (
      EditReplace      EditType = "replace"
      EditInsertAfter  EditType = "insert_after"
      EditInsertBefore EditType = "insert_before"
      EditDelete       EditType = "delete"
  )

  type Edit struct {
      Type    EditType `json:"type"`
      Anchor  string   `json:"anchor,omitempty"`
      Start   string   `json:"start,omitempty"`
      End     string   `json:"end,omitempty"`
      Content *string  `json:"content,omitempty"` // nil=absent, &""=explicit empty
  }

  type EditRequest struct {
      Path  string `json:"path"`
      Edits []Edit `json:"edits"`
  }

  type EditResult struct {
      Path         string `json:"path"`
      LinesChanged int    `json:"lines_changed"`
      Diff         string `json:"diff"`
  }

  type ReadRequest struct {
      Path   string `json:"path"`
      Offset *int   `json:"offset,omitempty"`
      Limit  *int   `json:"limit,omitempty"`
  }

  type WriteRequest struct {
      Path    string `json:"path"`
      Content string `json:"content"`
  }

  type SelfCorrectResult struct {
      Status      string `json:"status"`
      Message     string `json:"message"`
      FileContent string `json:"file_content"`
      Note        string `json:"note,omitempty"`
  }

Use unicode.IsLetter/unicode.IsDigit (NOT regexp \p{L}). Use xxhash.Checksum32S (NOT New32()).

## Tests (pkg/hashline/hashline_test.go)

- TestHashLine_Vectors: All Phase 0 vectors match
- TestHashLine_TrailingWhitespaceIgnored: "x := 1   " hashes same as "x := 1"
- TestHashLine_StructuralPositionSensitive: "}" at line 7 ≠ "}" at line 18
- TestHashLine_ContentPositionIndependent: "x := 1" at line 3 == "x := 1" at line 15
- TestHashLine_BOMExcluded: Line with BOM prefix hashes same as without
- TestParseRef_ValidNormal: "5#SN" → (5, "SN", nil)
- TestParseRef_ValidBOF: "0:" → (0, "", nil)
- TestParseRef_ValidEOF: "EOF:" → (-1, "", nil)
- TestParseRef_Rejects: "abc#ZZ", "5#zz", "5", "" all return error

## Acceptance Criteria

go test ./pkg/hashline/... -run TestHashLine_Vectors passes; all 9 named test functions pass; internal/editor/types.go compiles with Content *string

