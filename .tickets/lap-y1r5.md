---
id: lap-y1r5
status: closed
deps: [lap-8xgv, lap-n4yz]
links: []
created: 2026-04-04T04:17:10Z
type: task
priority: 2
assignee: Ronny Unger
tags: [wave-3, lapp]
---
# Issue 5: internal/editor (edit engine)

The editor applies a batch of edits to a []string line slice. Owns validation, sorting, overlap detection, no-op detection, sanitization, error formatting. Takes FileData from fileio and returns modified []string + result/error. No file I/O here.

Key functions:

  func ApplyEdits(fd *fileio.FileData, req *types.EditRequest) ([]string, *types.EditResult, string, string)
  func ValidateEdits(edits []types.Edit, lines []string) (map[int]int, string, string)
  func SortEditsBottomUp(edits []types.Edit, lineNums map[int]int) []types.Edit
  func DetectOverlaps(edits []types.Edit, lineNums map[int]int) [][2]int
  func IsNoOp(original, result []string) bool
  func FormatMismatchError(mismatches []RefMismatch, lines []string) string
  func SanitizeContent(content string) string
  func NormalizeNewlines(content string) string

PRE-MORTEM FIX pm-20260404-004: Remove attemptNum parameter. MCP is stateless — no consecutive-failure tracking. Note is always included:

  func BuildSelfCorrectResult(lines []string, limit int) *types.SelfCorrectResult {
      return &types.SelfCorrectResult{
          Status:      "needs_read_first",
          Message:     "No valid LINE#HASH references found. Use the file_content below to construct your edits.",
          FileContent: buildHashlineContent(lines, limit),
          Note:        "If unable to proceed after using these references, report the edit as blocked rather than retrying further.",
      }
  }

Content field validation rules (§6.2):
- replace: content required; empty string ("") means delete the line/range via replace (Content=&"")
- insert_after, insert_before: content required AND must be non-empty (Content="" → ERR_INVALID_EDIT)
- delete: content must be absent (Content must be nil, not &"")

Trailing newline in content (§6.3): "line1\nline2\n" → 2 lines, not 3. Discard the empty element
after the final split if content ends with \n.

ERR_INVALID_RANGE: if start line number > end line number, return ERR_INVALID_RANGE (not INVALID_EDIT).

Special anchor handling:
- lineNum==0 (ParseRef("0:")) → insert at position 0 (prepend)
- lineNum==-1 (ParseRef("EOF:")) → insert at len(lines) (append)

Sorting: bottom-up by descending line number, tie-break: delete > replace > insert_before > insert_after.
\\n normalization: strings.Contains(content, "\\\\n") → log once per call, then replace.
ERR_TOO_MANY_EDITS: reject batches > 100 edits.

## Tests (internal/editor/editor_test.go)

- TestApplyEdits_SingleReplace: Replace one line → correct diff
- TestApplyEdits_RangeReplace: Replace lines 2-4 → correct diff
- TestApplyEdits_InsertAfter: Insert after line 6 → correct result
- TestApplyEdits_InsertBefore: Insert before line 5 → correct result
- TestApplyEdits_DeleteSingle: Delete line 3 → line removed
- TestApplyEdits_DeleteRange: Delete lines 2-4 → all removed
- TestApplyEdits_MultiEditBatch: 3+ edits applied bottom-up correctly
- TestApplyEdits_InvalidFieldCombo: Anchor + Start → ERR_INVALID_EDIT
- TestApplyEdits_InsertAfterRangeRejected: insert_after with start/end → ERR_INVALID_EDIT
- TestApplyEdits_HashMismatch: Stale ref → full rejection with >>> markers and remap table
- TestApplyEdits_Overlapping: Overlapping ranges → ERR_OVERLAPPING_EDITS
- TestApplyEdits_TooManyEdits: 101 edits → ERR_TOO_MANY_EDITS
- TestApplyEdits_SameAnchorOrder: Two insert_after on same anchor → array order preserved
- TestApplyEdits_NoOp: Edits produce no change → ERR_NO_OP, no write
- TestApplyEdits_BOFAnchor: "0:" inserts at beginning of file
- TestApplyEdits_EOFAnchor: "EOF:" appends to end of file
- TestApplyEdits_EmptyFile_BOF: "0:" on empty file → content inserted
- TestSanitizeContent_StripsPrefixes: "6#PM:    if err" → "    if err"
- TestSanitizeContent_StripsDiffMarkers: All + prefixes stripped when all lines have +
- TestSanitizeContent_PartialMatchUnchanged: Mixed content not stripped
- TestNormalizeNewlines: "a\\nb" → "a\nb"
- TestBuildSelfCorrectResult: Note field always set (stateless — no attemptNum)
- TestBlankLinePositionSensitivity: Blank line ref fails after file shifts it
- TestApplyEdits_TrailingNewlineDiscarded: content "line1\nline2\n" → 2 inserted lines, not 3
- TestApplyEdits_ContentEmptyStringDelete: replace with Content=&"" removes the line
- TestApplyEdits_InvalidRange: start line > end line → ERR_INVALID_RANGE

## Acceptance Criteria

All 26 named test functions pass; go test ./internal/editor/... -count=1

