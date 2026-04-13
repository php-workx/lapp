package editor_test

import (
	"encoding/json"
	"io/fs"
	"strings"
	"testing"

	"github.com/lapp-dev/lapp/internal/editor"
	"github.com/lapp-dev/lapp/internal/fileio"
	"github.com/lapp-dev/lapp/pkg/hashline"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func strPtr(s string) *string { return &s }

// makeFileData builds a FileData backed by LF-terminated lines.
func makeFileData(lines []string) *fileio.FileData {
	terms := make([]string, len(lines))
	for i := range terms {
		if i < len(lines)-1 {
			terms[i] = "\n"
		}
		// last line: no terminator (unterminated)
	}
	return &fileio.FileData{
		Lines:          lines,
		Terminators:    terms,
		MajorityEnding: "\n",
		HasBOM:         false,
		Mode:           fs.FileMode(0644),
		CanonicalPath:  "test/file.go",
	}
}

// ref builds a "LINE#HASH" reference for lines[lineNum-1].
func ref(lines []string, lineNum int) string {
	return hashline.FormatLine(lines[lineNum-1], lineNum)[:strings.Index(hashline.FormatLine(lines[lineNum-1], lineNum), ":")]
}

func makeReq(path string, edits []editor.Edit) *editor.EditRequest {
	return &editor.EditRequest{Path: path, Edits: edits}
}

var testLines5 = []string{"alpha", "bravo", "charlie", "delta", "echo"}

// ── individual edit operations ────────────────────────────────────────────────

func TestApplyEdits_SingleReplace(t *testing.T) {
	lines := testLines5
	fd := makeFileData(lines)
	anchor := ref(lines, 3)
	req := makeReq("f.go", []editor.Edit{
		{Type: editor.EditReplace, Anchor: anchor, Content: strPtr("CHARLIE")},
	})
	newLines, result, code, detail := editor.ApplyEdits(fd, req)
	if code != "" {
		t.Fatalf("unexpected error %s: %s", code, detail)
	}
	if len(newLines) != 5 || newLines[2] != "CHARLIE" {
		t.Errorf("expected line 3 == CHARLIE, got %v", newLines)
	}
	if result.LinesChanged < 1 {
		t.Errorf("LinesChanged should be ≥1, got %d", result.LinesChanged)
	}
}

func TestApplyEdits_SingleReplacePreservesIndentation(t *testing.T) {
	lines := []string{
		"def f():",
		"        value = 1",
		"        return value",
	}
	fd := makeFileData(lines)
	anchor := ref(lines, 2)
	req := makeReq("f.py", []editor.Edit{
		{Type: editor.EditReplace, Anchor: anchor, Content: strPtr("            value = right")},
	})
	newLines, _, code, detail := editor.ApplyEdits(fd, req)
	if code != "" {
		t.Fatalf("unexpected error %s: %s", code, detail)
	}
	if newLines[1] != "        value = right" {
		t.Fatalf("expected indentation to be preserved, got %q", newLines[1])
	}
}

func TestApplyEdits_RangeReplace(t *testing.T) {
	lines := testLines5
	fd := makeFileData(lines)
	start := ref(lines, 2)
	end := ref(lines, 4)
	req := makeReq("f.go", []editor.Edit{
		{Type: editor.EditReplace, Start: start, End: end, Content: strPtr("X\nY")},
	})
	newLines, _, code, _ := editor.ApplyEdits(fd, req)
	if code != "" {
		t.Fatalf("error %s", code)
	}
	// alpha, X, Y, echo
	if len(newLines) != 4 || newLines[1] != "X" || newLines[2] != "Y" {
		t.Errorf("unexpected result: %v", newLines)
	}
}

func TestApplyEdits_InsertAfter(t *testing.T) {
	lines := testLines5
	fd := makeFileData(lines)
	anchor := ref(lines, 2)
	req := makeReq("f.go", []editor.Edit{
		{Type: editor.EditInsertAfter, Anchor: anchor, Content: strPtr("inserted")},
	})
	newLines, _, code, _ := editor.ApplyEdits(fd, req)
	if code != "" {
		t.Fatalf("error %s", code)
	}
	if len(newLines) != 6 || newLines[2] != "inserted" {
		t.Errorf("unexpected: %v", newLines)
	}
}

func TestApplyEdits_InsertBefore(t *testing.T) {
	lines := testLines5
	fd := makeFileData(lines)
	anchor := ref(lines, 3)
	req := makeReq("f.go", []editor.Edit{
		{Type: editor.EditInsertBefore, Anchor: anchor, Content: strPtr("before")},
	})
	newLines, _, code, _ := editor.ApplyEdits(fd, req)
	if code != "" {
		t.Fatalf("error %s", code)
	}
	if len(newLines) != 6 || newLines[2] != "before" || newLines[3] != "charlie" {
		t.Errorf("unexpected: %v", newLines)
	}
}

func TestApplyEdits_DeleteSingle(t *testing.T) {
	lines := testLines5
	fd := makeFileData(lines)
	anchor := ref(lines, 3)
	req := makeReq("f.go", []editor.Edit{
		{Type: editor.EditDelete, Anchor: anchor},
	})
	newLines, _, code, _ := editor.ApplyEdits(fd, req)
	if code != "" {
		t.Fatalf("error %s", code)
	}
	if len(newLines) != 4 {
		t.Fatalf("expected 4 lines, got %d: %v", len(newLines), newLines)
	}
	for _, l := range newLines {
		if l == "charlie" {
			t.Error("charlie should be deleted")
		}
	}
}

func TestApplyEdits_DeleteRange(t *testing.T) {
	lines := testLines5
	fd := makeFileData(lines)
	start := ref(lines, 2)
	end := ref(lines, 4)
	req := makeReq("f.go", []editor.Edit{
		{Type: editor.EditDelete, Start: start, End: end},
	})
	newLines, _, code, _ := editor.ApplyEdits(fd, req)
	if code != "" {
		t.Fatalf("error %s", code)
	}
	if len(newLines) != 2 || newLines[0] != "alpha" || newLines[1] != "echo" {
		t.Errorf("unexpected: %v", newLines)
	}
}

func TestApplyEdits_MultiEditBatch(t *testing.T) {
	// 5 lines; delete line 5, replace line 3, insert_after line 1.
	// Applied bottom-up: delete 5 first, then replace 3, then insert after 1.
	lines := testLines5
	fd := makeFileData(lines)
	req := makeReq("f.go", []editor.Edit{
		{Type: editor.EditInsertAfter, Anchor: ref(lines, 1), Content: strPtr("NEW")},
		{Type: editor.EditReplace, Anchor: ref(lines, 3), Content: strPtr("CHARLIE2")},
		{Type: editor.EditDelete, Anchor: ref(lines, 5)},
	})
	newLines, _, code, _ := editor.ApplyEdits(fd, req)
	if code != "" {
		t.Fatalf("error %s", code)
	}
	// Expected: alpha, NEW, bravo, CHARLIE2, delta (echo deleted)
	want := []string{"alpha", "NEW", "bravo", "CHARLIE2", "delta"}
	if len(newLines) != len(want) {
		t.Fatalf("len mismatch: got %v, want %v", newLines, want)
	}
	for i, v := range want {
		if newLines[i] != v {
			t.Errorf("[%d] got %q want %q", i, newLines[i], v)
		}
	}
}

// ── validation errors ─────────────────────────────────────────────────────────

func TestApplyEdits_InvalidFieldCombo(t *testing.T) {
	lines := testLines5
	fd := makeFileData(lines)
	anchor := ref(lines, 2)
	start := ref(lines, 2)
	_, _, code, _ := editor.ApplyEdits(fd, makeReq("f.go", []editor.Edit{
		{Type: editor.EditReplace, Anchor: anchor, Start: start, Content: strPtr("x")},
	}))
	if code != editor.ErrInvalidEdit {
		t.Errorf("expected ErrInvalidEdit, got %s", code)
	}
}

func TestApplyEdits_InsertAfterRangeRejected(t *testing.T) {
	lines := testLines5
	fd := makeFileData(lines)
	start := ref(lines, 2)
	end := ref(lines, 3)
	_, _, code, _ := editor.ApplyEdits(fd, makeReq("f.go", []editor.Edit{
		{Type: editor.EditInsertAfter, Start: start, End: end, Content: strPtr("x")},
	}))
	if code != editor.ErrInvalidEdit {
		t.Errorf("expected ErrInvalidEdit, got %s", code)
	}
}

func TestApplyEdits_HashMismatch(t *testing.T) {
	lines := testLines5
	fd := makeFileData(lines)
	// Build a ref with a deliberately wrong hash.
	goodRef := ref(lines, 3)
	badRef := goodRef[:strings.Index(goodRef, "#")+1] + "ZZ"
	_, _, code, detail := editor.ApplyEdits(fd, makeReq("f.go", []editor.Edit{
		{Type: editor.EditReplace, Anchor: badRef, Content: strPtr("x")},
	}))
	if code != editor.ErrStaleRefs {
		t.Errorf("expected ErrStaleRefs, got %s", code)
	}
	var payload editor.StaleRefRepairResult
	if err := json.Unmarshal([]byte(detail), &payload); err != nil {
		t.Fatalf("expected stale payload JSON, got: %s\nerr: %v", detail, err)
	}
	if payload.Status != "stale_refs" || payload.ErrorCode != editor.ErrHashMismatch || len(payload.Changed) == 0 {
		t.Fatalf("unexpected stale payload: %+v", payload)
	}
}

func TestApplyEdits_Overlapping(t *testing.T) {
	lines := testLines5
	fd := makeFileData(lines)
	// Two replace ops covering overlapping ranges.
	_, _, code, _ := editor.ApplyEdits(fd, makeReq("f.go", []editor.Edit{
		{Type: editor.EditReplace, Start: ref(lines, 2), End: ref(lines, 4), Content: strPtr("x")},
		{Type: editor.EditReplace, Anchor: ref(lines, 3), Content: strPtr("y")},
	}))
	if code != editor.ErrOverlappingEdits {
		t.Errorf("expected ErrOverlappingEdits, got %s", code)
	}
}

func TestApplyEdits_TooManyEdits(t *testing.T) {
	// Build 101 edits on a large file.
	lines := make([]string, 200)
	for i := range lines {
		lines[i] = "line"
	}
	fd := makeFileData(lines)
	edits := make([]editor.Edit, 101)
	for i := range edits {
		edits[i] = editor.Edit{Type: editor.EditReplace, Anchor: ref(lines, i+1), Content: strPtr("x")}
	}
	_, _, code, _ := editor.ApplyEdits(fd, makeReq("f.go", edits))
	if code != editor.ErrTooManyEdits {
		t.Errorf("expected ErrTooManyEdits, got %s", code)
	}
}

func TestApplyEdits_SameAnchorOrder(t *testing.T) {
	// Two insert_after on line 2 → both applied; first insert appears before second.
	lines := testLines5
	fd := makeFileData(lines)
	anchor := ref(lines, 2)
	req := makeReq("f.go", []editor.Edit{
		{Type: editor.EditInsertAfter, Anchor: anchor, Content: strPtr("FIRST")},
		{Type: editor.EditInsertAfter, Anchor: anchor, Content: strPtr("SECOND")},
	})
	newLines, _, code, _ := editor.ApplyEdits(fd, req)
	if code != "" {
		t.Fatalf("error %s", code)
	}
	// alpha, bravo, FIRST, SECOND, charlie, delta, echo
	if len(newLines) != 7 {
		t.Fatalf("expected 7 lines, got %d: %v", len(newLines), newLines)
	}
	if newLines[2] != "FIRST" || newLines[3] != "SECOND" {
		t.Errorf("order wrong: %v", newLines)
	}
}

func TestApplyEdits_NoOp(t *testing.T) {
	lines := testLines5
	fd := makeFileData(lines)
	anchor := ref(lines, 3)
	// Replace line 3 with its own content → no change.
	req := makeReq("f.go", []editor.Edit{
		{Type: editor.EditReplace, Anchor: anchor, Content: strPtr("charlie")},
	})
	_, _, code, _ := editor.ApplyEdits(fd, req)
	if code != editor.ErrNoOp {
		t.Errorf("expected ErrNoOp, got %s", code)
	}
}

// ── special anchors ───────────────────────────────────────────────────────────

func TestApplyEdits_BOFAnchor(t *testing.T) {
	lines := testLines5
	fd := makeFileData(lines)
	req := makeReq("f.go", []editor.Edit{
		{Type: editor.EditInsertAfter, Anchor: "0:", Content: strPtr("PREPENDED")},
	})
	newLines, _, code, _ := editor.ApplyEdits(fd, req)
	if code != "" {
		t.Fatalf("error %s", code)
	}
	if newLines[0] != "PREPENDED" {
		t.Errorf("expected first line PREPENDED, got %v", newLines)
	}
}

func TestApplyEdits_EOFAnchor(t *testing.T) {
	lines := testLines5
	fd := makeFileData(lines)
	req := makeReq("f.go", []editor.Edit{
		{Type: editor.EditInsertAfter, Anchor: "EOF:", Content: strPtr("APPENDED")},
	})
	newLines, _, code, _ := editor.ApplyEdits(fd, req)
	if code != "" {
		t.Fatalf("error %s", code)
	}
	if newLines[len(newLines)-1] != "APPENDED" {
		t.Errorf("expected last line APPENDED, got %v", newLines)
	}
}

func TestApplyEdits_EmptyFile_BOF(t *testing.T) {
	// Empty file represented as single empty line.
	lines := []string{""}
	fd := makeFileData(lines)
	req := makeReq("f.go", []editor.Edit{
		{Type: editor.EditInsertAfter, Anchor: "0:", Content: strPtr("hello")},
	})
	newLines, _, code, _ := editor.ApplyEdits(fd, req)
	if code != "" {
		t.Fatalf("error %s", code)
	}
	if len(newLines) == 0 || newLines[0] != "hello" {
		t.Errorf("expected hello as first line, got %v", newLines)
	}
}

// ── sanitization ─────────────────────────────────────────────────────────────

func TestSanitizeContent_StripsPrefixes(t *testing.T) {
	// All lines have hashline prefix → strip.
	input := "6#PM:    if err != nil {\n7#WY:        return err\n8#QW:    }"
	got := editor.SanitizeContent(input)
	want := "    if err != nil {\n        return err\n    }"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSanitizeContent_StripsDiffMarkers(t *testing.T) {
	input := "+    if err != nil {\n+        return err\n+    }"
	got := editor.SanitizeContent(input)
	want := "    if err != nil {\n        return err\n    }"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSanitizeContent_PartialMatchUnchanged(t *testing.T) {
	// Only some lines have prefix → do not strip.
	input := "6#PM:line one\nnormal line"
	got := editor.SanitizeContent(input)
	if got != input {
		t.Errorf("should be unchanged, got %q", got)
	}
}

func TestNormalizeNewlines(t *testing.T) {
	// Literal backslash-n should become real newline.
	input := `a\nb`
	got := editor.NormalizeNewlines(input)
	if got != "a\nb" {
		t.Errorf("got %q, want real newline", got)
	}
}

func TestBuildSelfCorrectResult(t *testing.T) {
	lines := []string{"func main() {", "    fmt.Println(\"hello\")", "}"}
	result := editor.BuildSelfCorrectResult(lines, 100, "")
	if result.Status != "needs_read_first" {
		t.Errorf("Status = %q, want needs_read_first", result.Status)
	}
	if result.Note == "" {
		t.Error("Note should always be set (stateless)")
	}
	if !strings.Contains(result.FileContent, "1#") {
		t.Error("FileContent should contain hashline refs")
	}
}

func TestBuildSelfCorrectResult_UsesExplicitMessage(t *testing.T) {
	lines := []string{"a", "b"}
	result := editor.BuildSelfCorrectResult(lines, 100, "Ref \"245\" is missing the #HASH part.")
	if result.Message != "Ref \"245\" is missing the #HASH part." {
		t.Fatalf("Message = %q", result.Message)
	}
}

func TestBlankLinePositionSensitivity(t *testing.T) {
	// File: ["a", "", "b"]. Blank line at position 2 has hash H(2).
	// Insert a line before position 2 → blank shifts to line 3 → old ref "2#H(2)" is stale.
	lines := []string{"a", "", "b"}
	fd := makeFileData(lines)
	blankRef := ref(lines, 2) // "2#<hash>"
	// Insert before line 2 to shift the blank line.
	insertOp := editor.Edit{Type: editor.EditInsertBefore, Anchor: ref(lines, 2), Content: strPtr("inserted")}
	req1 := makeReq("f.go", []editor.Edit{insertOp})
	newLines, _, code, _ := editor.ApplyEdits(fd, req1)
	if code != "" {
		t.Fatalf("insert failed: %s", code)
	}
	// Now try to use old blankRef on the modified file.
	fd2 := makeFileData(newLines)
	req2 := makeReq("f.go", []editor.Edit{
		{Type: editor.EditDelete, Anchor: blankRef},
	})
	_, _, code2, _ := editor.ApplyEdits(fd2, req2)
	if code2 != editor.ErrStaleRefs {
		t.Errorf("expected ErrStaleRefs after line shift, got %s", code2)
	}
}

// ── content splitting ─────────────────────────────────────────────────────────

func TestApplyEdits_TrailingNewlineDiscarded(t *testing.T) {
	// content "line1\nline2\n" should produce 2 inserted lines, not 3.
	lines := testLines5
	fd := makeFileData(lines)
	content := "X\nY\n" // trailing newline
	req := makeReq("f.go", []editor.Edit{
		{Type: editor.EditInsertAfter, Anchor: "EOF:", Content: &content},
	})
	newLines, _, code, _ := editor.ApplyEdits(fd, req)
	if code != "" {
		t.Fatalf("error %s", code)
	}
	// Should append X and Y (2 lines), not X, Y, "" (3 lines).
	n := len(newLines)
	if newLines[n-2] != "X" || newLines[n-1] != "Y" {
		t.Errorf("last two lines should be X, Y; got %v", newLines[n-2:])
	}
}

func TestApplyEdits_ContentEmptyStringDelete(t *testing.T) {
	// replace with Content=&"" should remove the line.
	lines := testLines5
	fd := makeFileData(lines)
	anchor := ref(lines, 3)
	empty := ""
	req := makeReq("f.go", []editor.Edit{
		{Type: editor.EditReplace, Anchor: anchor, Content: &empty},
	})
	newLines, _, code, _ := editor.ApplyEdits(fd, req)
	if code != "" {
		t.Fatalf("error %s", code)
	}
	if len(newLines) != 4 {
		t.Errorf("expected 4 lines after replace-as-delete, got %d: %v", len(newLines), newLines)
	}
	for _, l := range newLines {
		if l == "charlie" {
			t.Error("charlie should be deleted")
		}
	}
}

func TestApplyEdits_InvalidRange(t *testing.T) {
	// start line > end line → ERR_INVALID_RANGE.
	lines := testLines5
	fd := makeFileData(lines)
	// Swap start and end intentionally.
	_, _, code, _ := editor.ApplyEdits(fd, makeReq("f.go", []editor.Edit{
		{Type: editor.EditReplace, Start: ref(lines, 4), End: ref(lines, 2), Content: strPtr("x")},
	}))
	if code != editor.ErrInvalidRange {
		t.Errorf("expected ErrInvalidRange, got %s", code)
	}
}


// ── wave 2 bugfix tests ──────────────────────────────────────────────

// lap-1u6m: ERR_LINE_OUT_OF_RANGE instead of ERR_HASH_MISMATCH for out-of-bounds refs.
func TestApplyEdits_LineOutOfRange(t *testing.T) {
	lines := testLines5 // 5 lines
	fd := makeFileData(lines)
	// Reference line 10 in a 5-line file.
	_, _, code, detail := editor.ApplyEdits(fd, makeReq("f.go", []editor.Edit{
		{Type: editor.EditDelete, Anchor: "10#ZZ"},
	}))
	if code != editor.ErrLineOutOfRange {
		t.Errorf("expected ErrLineOutOfRange for line 10 in 5-line file, got %s", code)
	}
	if !strings.Contains(detail, "out of range") {
		t.Errorf("detail should mention out of range, got: %s", detail)
	}
}

func TestApplyEdits_RangeEndOutOfRange(t *testing.T) {
	lines := testLines5 // 5 lines
	fd := makeFileData(lines)
	// Range end line is beyond file.
	_, _, code, _ := editor.ApplyEdits(fd, makeReq("f.go", []editor.Edit{
		{Type: editor.EditDelete, Start: ref(lines, 3), End: "8#YY"},
	}))
	if code != editor.ErrLineOutOfRange {
		t.Errorf("expected ErrLineOutOfRange for end=8 in 5-line file, got %s", code)
	}
}

func TestApplyEdits_HashMismatchNotLineOutOfRange(t *testing.T) {
	lines := testLines5 // 5 lines
	fd := makeFileData(lines)
	// Line 3 exists but hash is wrong — must be ERR_HASH_MISMATCH, not out-of-range.
	badRef := strings.Replace(ref(lines, 3), ref(lines, 3)[strings.Index(ref(lines, 3), "#")+1:], "ZZ", 1)
	_, _, code, detail := editor.ApplyEdits(fd, makeReq("f.go", []editor.Edit{
		{Type: editor.EditDelete, Anchor: badRef},
	}))
	if code != editor.ErrStaleRefs {
		t.Errorf("expected ErrStaleRefs for valid line with wrong hash, got %s", code)
	}
	var payload editor.StaleRefRepairResult
	if err := json.Unmarshal([]byte(detail), &payload); err != nil {
		t.Fatalf("expected stale payload JSON, got: %s\nerr: %v", detail, err)
	}
}

func TestApplyEdits_OutOfRangePriorityOverMismatch(t *testing.T) {
	// Batch: edit 1 out-of-range, edit 2 hash mismatch → ERR_LINE_OUT_OF_RANGE wins.
	lines := testLines5
	fd := makeFileData(lines)
	badRef := strings.Replace(ref(lines, 3), ref(lines, 3)[strings.Index(ref(lines, 3), "#")+1:], "ZZ", 1)
	_, _, code, _ := editor.ApplyEdits(fd, makeReq("f.go", []editor.Edit{
		{Type: editor.EditReplace, Anchor: "10#ZZ", Content: strPtr("x")}, // out of range
		{Type: editor.EditDelete, Anchor: badRef},                           // hash mismatch
	}))
	if code != editor.ErrLineOutOfRange {
		t.Errorf("expected ErrLineOutOfRange to take priority, got %s", code)
	}
}

// lap-sl1c: BOF/EOF anchors rejected on insert_before.
func TestApplyEdits_InsertBefore_BOF_Rejected(t *testing.T) {
	lines := testLines5
	fd := makeFileData(lines)
	_, _, code, _ := editor.ApplyEdits(fd, makeReq("f.go", []editor.Edit{
		{Type: editor.EditInsertBefore, Anchor: "0:", Content: strPtr("x")},
	}))
	if code != editor.ErrInvalidEdit {
		t.Errorf("expected ErrInvalidEdit for insert_before with BOF anchor, got %s", code)
	}
}

func TestApplyEdits_InsertBefore_EOF_Rejected(t *testing.T) {
	lines := testLines5
	fd := makeFileData(lines)
	_, _, code, _ := editor.ApplyEdits(fd, makeReq("f.go", []editor.Edit{
		{Type: editor.EditInsertBefore, Anchor: "EOF:", Content: strPtr("x")},
	}))
	if code != editor.ErrInvalidEdit {
		t.Errorf("expected ErrInvalidEdit for insert_before with EOF anchor, got %s", code)
	}
}

func TestApplyEdits_InsertAfter_BOF_EOF_StillWork(t *testing.T) {
	// Verify insert_after with BOF/EOF still works correctly.
	lines := testLines5
	fd := makeFileData(lines)
	// insert_after BOF
	nl, _, code, _ := editor.ApplyEdits(fd, makeReq("f.go", []editor.Edit{
		{Type: editor.EditInsertAfter, Anchor: "0:", Content: strPtr("PREPEND")},
	}))
	if code != "" {
		t.Fatalf("insert_after BOF failed: %s", code)
	}
	if nl[0] != "PREPEND" {
		t.Errorf("expected PREPEND as first line, got %v", nl)
	}
}

// lap-ccwb: NormalizeNewlines preserves intentional backslash-n in code.
func TestNormalizeNewlines_PreservesCodeBackslashN(t *testing.T) {
	// content with real newline AND literal backslash-n: no normalization.
	content := "fmt.Println(\"hello\\nworld\")" + "\n" + "return nil"
	got := editor.NormalizeNewlines(content)
	if got != content {
		t.Errorf("should not normalize when real newlines present; got %q", got)
	}
}

func TestNormalizeNewlines_NormalizesWhenNoRealNewlines(t *testing.T) {
	// content with only literal backslash-n and no real newlines: normalize.
	content := `line1\nline2\nline3`
	got := editor.NormalizeNewlines(content)
	if !strings.Contains(got, "\n") {
		t.Errorf("expected real newlines after normalization, got %q", got)
	}
	parts := strings.Split(got, "\n")
	if len(parts) != 3 {
		t.Errorf("expected 3 lines after normalization, got %d: %v", len(parts), parts)
	}
}

func TestNormalizeNewlines_EmptyString(t *testing.T) {
	if got := editor.NormalizeNewlines(""); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}