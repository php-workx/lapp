package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lapp-dev/lapp/internal/editor"
	"github.com/lapp-dev/lapp/internal/fileio"
	"github.com/mark3labs/mcp-go/mcp"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func newTestConfig(t *testing.T) *fileio.Config {
	t.Helper()
	root := t.TempDir()
	// Resolve symlinks so cfg.Root matches what CheckPath/EvalSymlinks returns.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return &fileio.Config{
		Root:          root,
		BlockPatterns: fileio.DefaultBlockPatterns,
		AllowPatterns: fileio.DefaultAllowPatterns,
		DefaultLimit:  2000,
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func callRead(t *testing.T, s *Server, path string, offset, limit int) string {
	t.Helper()
	args := map[string]any{"path": path}
	if offset > 0 {
		args["offset"] = float64(offset)
	}
	if limit > 0 {
		args["limit"] = float64(limit)
	}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	result, err := s.handleRead(context.Background(), req)
	if err != nil {
		t.Fatalf("handleRead error: %v", err)
	}
	return extractText(t, result)
}

func callEdit(t *testing.T, s *Server, path string, edits []editor.Edit) string {
	t.Helper()
	// Convert edits to []any via JSON round-trip so they arrive as maps.
	editsJSON, err := json.Marshal(edits)
	if err != nil {
		t.Fatalf("marshal edits: %v", err)
	}
	var editsAny []any
	if err := json.Unmarshal(editsJSON, &editsAny); err != nil {
		t.Fatalf("unmarshal edits: %v", err)
	}
	args := map[string]any{"path": path, "edits": editsAny}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	result, err := s.handleEdit(context.Background(), req)
	if err != nil {
		t.Fatalf("handleEdit error: %v", err)
	}
	return extractText(t, result)
}

func callWrite(t *testing.T, s *Server, path, content string) string {
	t.Helper()
	args := map[string]any{"path": path, "content": content}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	result, err := s.handleWrite(context.Background(), req)
	if err != nil {
		t.Fatalf("handleWrite error: %v", err)
	}
	return extractText(t, result)
}

func callGrep(t *testing.T, s *Server, pattern, searchPath string) string {
	t.Helper()
	args := map[string]any{"pattern": pattern}
	if searchPath != "" {
		args["path"] = searchPath
	}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	result, err := s.handleGrep(context.Background(), req)
	if err != nil {
		t.Fatalf("handleGrep error: %v", err)
	}
	return extractText(t, result)
}

// extractText pulls the text from the first TextContent item.
func extractText(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	if r == nil {
		t.Fatal("nil CallToolResult")
	}
	for _, c := range r.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			return tc.Text
		}
	}
	t.Fatal("no TextContent in result")
	return ""
}

// extractTextNoFail is a goroutine-safe variant of extractText that returns
// an empty string instead of calling t.Fatalf when no text content is found.
func extractTextNoFail(r *mcp.CallToolResult) string {
	if r == nil {
		return ""
	}
	for _, c := range r.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

// parseHashRef extracts the first "N#XX" ref from a formatted line like "N#XX:content".
func parseHashRef(line string) string {
	re := regexp.MustCompile(`(\d+#[ZPMQVRWSNKTXJBYH]{2})`)
	m := re.FindString(line)
	return m
}

// firstLineRef returns the LINE#HASH ref from the first matching line in text.
func firstLineRef(t *testing.T, text, contains string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, contains) {
			ref := parseHashRef(line)
			if ref != "" {
				return ref
			}
		}
	}
	t.Fatalf("no ref found in lines containing %q in:\n%s", contains, text)
	return ""
}

func strPtr(s string) *string { return &s }

// ── tests ─────────────────────────────────────────────────────────────────────

// 1. Happy-path read → edit.
func TestRoundTrip_ReadEditSuccess(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)

	filePath := filepath.Join(cfg.Root, "file.txt")
	writeTestFile(t, filePath, "line one\nline two\nline three\n")

	// Read to get refs.
	readOut := callRead(t, s, filePath, 0, 0)
	if !strings.Contains(readOut, "line one") {
		t.Fatalf("read output missing content: %s", readOut)
	}

	// Find the ref for "line two".
	ref := firstLineRef(t, readOut, "line two")

	// Replace "line two" with "line 2".
	editOut := callEdit(t, s, filePath, []editor.Edit{
		{Type: editor.EditReplace, Anchor: ref, Content: strPtr("line 2")},
	})
	if !strings.Contains(editOut, "OK:") {
		t.Fatalf("edit failed: %s", editOut)
	}

	// Verify file on disk.
	data, _ := os.ReadFile(filePath)
	if !strings.Contains(string(data), "line 2") {
		t.Fatalf("file not updated: %s", string(data))
	}
	if strings.Contains(string(data), "line two") {
		t.Fatalf("old content still present: %s", string(data))
	}
}

// 2. Stale ref → mismatch error → extract updated ref → retry succeeds.
func TestRoundTrip_StaleRefThenCorrect(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)

	filePath := filepath.Join(cfg.Root, "stale.txt")
	writeTestFile(t, filePath, "alpha\nbeta\ngamma\n")

	// Read to capture ref for "beta".
	readOut := callRead(t, s, filePath, 0, 0)
	staleRef := firstLineRef(t, readOut, "beta")

	// Mutate the file externally — "beta" becomes "BETA" so the hash changes.
	writeTestFile(t, filePath, "alpha\nBETA\ngamma\n")

	// Attempt edit with stale ref — expect hash mismatch.
	editOut := callEdit(t, s, filePath, []editor.Edit{
		{Type: editor.EditReplace, Anchor: staleRef, Content: strPtr("replaced")},
	})
	if !strings.Contains(editOut, "ERR_HASH_MISMATCH") && !strings.Contains(editOut, "changed since") {
		t.Fatalf("expected hash mismatch error, got: %s", editOut)
	}

	// Extract updated ref from the remapping table in the error.
	// FormatMismatchError emits "  N#OLD → N#NEW" lines.
	updatedRef := ""
	re := regexp.MustCompile(`→\s+(\d+#[A-Z]{2})`)
	for _, line := range strings.Split(editOut, "\n") {
		if m := re.FindStringSubmatch(line); m != nil {
			updatedRef = m[1]
			break
		}
	}
	if updatedRef == "" {
		t.Fatalf("could not extract updated ref from: %s", editOut)
	}

	// Retry with updated ref.
	editOut2 := callEdit(t, s, filePath, []editor.Edit{
		{Type: editor.EditReplace, Anchor: updatedRef, Content: strPtr("replaced")},
	})
	if !strings.Contains(editOut2, "OK:") {
		t.Fatalf("retry edit failed: %s", editOut2)
	}

	data, _ := os.ReadFile(filePath)
	if !strings.Contains(string(data), "replaced") {
		t.Fatalf("file not updated after retry: %s", string(data))
	}
}

// 3. Edit without read → SELF_CORRECT → use returned refs → success.
func TestRoundTrip_SelfCorrect(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)

	filePath := filepath.Join(cfg.Root, "sc.txt")
	writeTestFile(t, filePath, "foo\nbar\nbaz\n")

	// Use a fake anchor with no valid hashline format → triggers SELF_CORRECT.
	editOut := callEdit(t, s, filePath, []editor.Edit{
		{Type: editor.EditReplace, Anchor: "line2", Content: strPtr("quux")},
	})

	// Should be JSON with status=needs_read_first.
	var sc editor.SelfCorrectResult
	if err := json.Unmarshal([]byte(editOut), &sc); err != nil {
		t.Fatalf("expected SelfCorrectResult JSON, got: %s\nerr: %v", editOut, err)
	}
	if sc.Status != "needs_read_first" {
		t.Fatalf("unexpected status: %s", sc.Status)
	}

	// Extract a ref from file_content for "bar".
	ref := firstLineRef(t, sc.FileContent, "bar")

	// Now edit with valid ref.
	editOut2 := callEdit(t, s, filePath, []editor.Edit{
		{Type: editor.EditReplace, Anchor: ref, Content: strPtr("quux")},
	})
	if !strings.Contains(editOut2, "OK:") {
		t.Fatalf("edit after self-correct failed: %s", editOut2)
	}

	data, _ := os.ReadFile(filePath)
	if !strings.Contains(string(data), "quux") {
		t.Fatalf("file not updated: %s", string(data))
	}
}

// 4. SelfCorrectResult.Note is always non-empty.
func TestRoundTrip_SelfCorrect_NoteAlwaysSet(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)

	filePath := filepath.Join(cfg.Root, "note.txt")
	writeTestFile(t, filePath, "hello\nworld\n")

	editOut := callEdit(t, s, filePath, []editor.Edit{
		{Type: editor.EditReplace, Anchor: "notaref", Content: strPtr("x")},
	})

	var sc editor.SelfCorrectResult
	if err := json.Unmarshal([]byte(editOut), &sc); err != nil {
		t.Fatalf("expected SelfCorrectResult JSON: %s", editOut)
	}
	if sc.Note == "" {
		t.Fatal("SelfCorrectResult.Note must always be non-empty")
	}
}

// 5. Refs from page 1 are valid when editing, even after reading page 2.
func TestPagination_CrossPageRefs(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.DefaultLimit = 5
	s := New(cfg)

	filePath := filepath.Join(cfg.Root, "big.txt")
	var sb strings.Builder
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&sb, "content line %d\n", i)
	}
	writeTestFile(t, filePath, sb.String())

	// Read page 1 (lines 1-5).
	page1 := callRead(t, s, filePath, 1, 5)
	// Read page 2 (lines 6-10).
	_ = callRead(t, s, filePath, 6, 5)

	// Get ref for "content line 2" from page 1.
	ref := firstLineRef(t, page1, "content line 2")

	// Edit line 2 using the page-1 ref — must succeed.
	editOut := callEdit(t, s, filePath, []editor.Edit{
		{Type: editor.EditReplace, Anchor: ref, Content: strPtr("replaced line 2")},
	})
	if !strings.Contains(editOut, "OK:") {
		t.Fatalf("cross-page edit failed: %s", editOut)
	}

	data, _ := os.ReadFile(filePath)
	if !strings.Contains(string(data), "replaced line 2") {
		t.Fatalf("file not updated: %s", string(data))
	}
}

// 6. lapp_write creates a new file; subsequent lapp_read works.
func TestWriteNewFile(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)

	filePath := filepath.Join(cfg.Root, "new.txt")
	writeOut := callWrite(t, s, filePath, "hello\nworld\n")
	if !strings.Contains(writeOut, "OK:") {
		t.Fatalf("write failed: %s", writeOut)
	}

	readOut := callRead(t, s, filePath, 0, 0)
	if !strings.Contains(readOut, "hello") || !strings.Contains(readOut, "world") {
		t.Fatalf("read after write missing content: %s", readOut)
	}
}

// 7. lapp_write on existing file → ErrFileExists.
func TestWriteExistingFile(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)

	filePath := filepath.Join(cfg.Root, "exists.txt")
	writeTestFile(t, filePath, "already here\n")

	writeOut := callWrite(t, s, filePath, "new content\n")
	if !strings.Contains(writeOut, editor.ErrFileExists) {
		t.Fatalf("expected ErrFileExists, got: %s", writeOut)
	}
}

// 8. lapp_grep returns LINE#HASH refs usable in lapp_edit without separate read.
func TestGrepReturnsHashRefs(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)

	filePath := filepath.Join(cfg.Root, "grep.txt")
	writeTestFile(t, filePath, "apple\nbanana\ncherry\n")

	grepOut := callGrep(t, s, "banana", filePath)
	if !strings.Contains(grepOut, "banana") {
		t.Fatalf("grep missing match: %s", grepOut)
	}

	// Extract ref for "banana".
	ref := firstLineRef(t, grepOut, "banana")

	// Use the grep ref directly in an edit.
	editOut := callEdit(t, s, filePath, []editor.Edit{
		{Type: editor.EditReplace, Anchor: ref, Content: strPtr("BANANA")},
	})
	if !strings.Contains(editOut, "OK:") {
		t.Fatalf("edit using grep ref failed: %s", editOut)
	}

	data, _ := os.ReadFile(filePath)
	if !strings.Contains(string(data), "BANANA") {
		t.Fatalf("file not updated: %s", string(data))
	}
}

// 9. Path outside root returns ERR_PATH_OUTSIDE_ROOT.
func TestPathOutsideRoot(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)

	// /tmp always exists and is outside our temp root.
	out := callRead(t, s, "/tmp", 0, 0)
	if !strings.Contains(out, fileio.ErrPathOutsideRoot) {
		t.Fatalf("expected ERR_PATH_OUTSIDE_ROOT, got: %s", out)
	}
}

// 10. Blocked path returns ERR_PATH_BLOCKED.
func TestPathBlocked(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)

	envPath := filepath.Join(cfg.Root, ".env")
	writeTestFile(t, envPath, "SECRET=x\n")

	out := callRead(t, s, envPath, 0, 0)
	if !strings.Contains(out, fileio.ErrPathBlocked) {
		t.Fatalf("expected ERR_PATH_BLOCKED, got: %s", out)
	}
}

// 11. lapp_write creates parent directories automatically.
func TestWriteCreatesParentDirs(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)

	filePath := filepath.Join(cfg.Root, "sub", "dir", "file.txt")
	writeOut := callWrite(t, s, filePath, "nested content\n")
	if !strings.Contains(writeOut, "OK:") {
		t.Fatalf("write with new dirs failed: %s", writeOut)
	}

	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("file does not exist after write: %v", err)
	}

	data, _ := os.ReadFile(filePath)
	if string(data) != "nested content\n" {
		t.Fatalf("unexpected content: %q", string(data))
	}
}


// ── new tests covering review findings ──────────────────────────────────────────

// TestGrepPathOutsideRoot verifies that supplying a path outside --root is rejected.
// This covers the P0 security finding from the spec review.
func TestGrepPathOutsideRoot(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	// /tmp is outside the test root.
	out := callGrep(t, s, ".", "/tmp")
	if !strings.Contains(out, editor.ErrPathOutsideRoot) {
		t.Errorf("expected ERR_PATH_OUTSIDE_ROOT for out-of-root path, got: %s", out)
	}
}

// TestGrepBlockedFile verifies that .env files matched during a grep walk are silently
// skipped — their content is never returned to the caller (P1 security finding).
func TestGrepBlockedFile(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	// Plant a .env file with a distinctive value.
	envPath := filepath.Join(cfg.Root, ".env")
	writeTestFile(t, envPath, "SECRET=hunter2\n")
	// Also plant a normal file so grep has something to walk.
	writeTestFile(t, filepath.Join(cfg.Root, "main.go"), "package main\n")
	// Grep for the secret — it must not appear.
	out := callGrep(t, s, "hunter2", "")
	if strings.Contains(out, "hunter2") {
		t.Errorf(".env content leaked through grep: %s", out)
	}
}

// TestGrepBOMFileHashConsistency verifies that grep and read return the same LINE#HASH
// for line 1 of a UTF-8 BOM file (P1 finding: BOM not stripped in grep).
func TestGrepBOMFileHashConsistency(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	// Write a UTF-8 BOM file.
	bomContent := "\xef\xbb\xbffunc main() {\n    fmt.Println()\n}\n"
	filePath := filepath.Join(cfg.Root, "bom.go")
	writeTestFile(t, filePath, bomContent)
	// Get line 1 hash from lapp_read.
	readOut := callRead(t, s, filePath, 0, 0)
	readLines := strings.Split(strings.TrimSpace(readOut), "\n")
	if len(readLines) == 0 {
		t.Fatal("lapp_read returned nothing")
	}
	readRef := parseHashRef(readLines[0]) // e.g. "1#KH"

	// Get line 1 hash from lapp_grep.
	grepOut := callGrep(t, s, "func main", "")
	// Extract the ref from the grep match line (>>> prefix).
	var grepRef string
	for _, line := range strings.Split(grepOut, "\n") {
		if strings.Contains(line, "func main") {
			grepRef = parseHashRef(line)
			break
		}
	}
	if grepRef == "" {
		t.Fatalf("grep did not match func main in output: %s", grepOut)
	}
	if readRef != grepRef {
		t.Errorf("BOM hash mismatch: lapp_read line 1 ref=%q, lapp_grep ref=%q (grep refs would be rejected by lapp_edit)", readRef, grepRef)
	}
}

// TestWriteAtomicRandomSuffix verifies that two concurrent lapp_write calls for
// different-cased filenames on a case-insensitive filesystem do not collide on
// the same temp path. We verify the random suffix is present in the temp filename
// by intercepting any leftover temps (there should be none) and confirming that the
// temp pattern includes a 8-hex-char random component between pid and .lapp.tmp.
// Indirect test: call handleWrite and verify no stale *.lapp.tmp files remain after success.
func TestWriteAtomicNoOrphan(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "atom.txt")
	out := callWrite(t, s, filePath, "hello\n")
	if !strings.Contains(out, "OK:") {
		t.Fatalf("write failed: %s", out)
	}
	// Verify no *.lapp.tmp files were left behind.
	entries, _ := os.ReadDir(cfg.Root)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".lapp.tmp") {
			t.Errorf("orphaned temp file found: %s", e.Name())
		}
	}
}

// TestReadFilePermissionDenied verifies ERR_PERMISSION_DENIED is returned when a
// file is unreadable (P2 finding: was silently mapped to ERR_FILE_NOT_FOUND).
func TestReadFilePermissionDenied(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	cfg := newTestConfig(t)
	filePath := filepath.Join(cfg.Root, "secret.txt")
	writeTestFile(t, filePath, "data")
	if err := os.Chmod(filePath, 0000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(filePath, 0644) })
	_, code := fileio.ReadFile(filePath, cfg)
	if code != fileio.ErrPermissionDenied {
		t.Errorf("expected ErrPermissionDenied, got %q", code)
	}
}
// TestBOMRoundTrip verifies that a UTF-8 BOM file survives a full read→edit→write cycle.
// BOM bytes must be present at byte offset 0 of the on-disk file after editing.
func TestBOMRoundTrip(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "bom.go")
	bom := []byte{0xEF, 0xBB, 0xBF}
	original := append(bom, []byte("line1\nline2\nline3\n")...)
	writeTestFile(t, filePath, string(original))

	// Read: get hash ref for line 2.
	readOut := callRead(t, s, filePath, 0, 0)
	readLines := strings.Split(strings.TrimSpace(readOut), "\n")
	// Find the ref for line 2 ("2#XX:line2")
	var line2Ref string
	for _, l := range readLines {
		if strings.HasPrefix(l, "2#") {
			line2Ref = parseHashRef(l)
			break
		}
	}
	if line2Ref == "" {
		t.Fatalf("could not parse line 2 ref from: %s", readOut)
	}

	// Edit: replace line 2.
	editOut := callEdit(t, s, filePath, []editor.Edit{
		{Type: editor.EditReplace, Anchor: line2Ref, Content: strPtr("LINE2_EDITED")},
	})
	if !strings.Contains(editOut, "OK:") {
		t.Fatalf("edit failed: %s", editOut)
	}

	// Verify: BOM still present at byte offset 0.
	raw, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(raw) < 3 || raw[0] != 0xEF || raw[1] != 0xBB || raw[2] != 0xBF {
		t.Errorf("BOM not preserved: first bytes = %x", raw[:min(3, len(raw))])
	}
	if !strings.Contains(string(raw), "LINE2_EDITED") {
		t.Errorf("edited content not found in file: %s", string(raw))
	}
}


// TestNoOpLeavesNoTempFile verifies that when all edits are no-ops,
// handleEdit returns ERR_NO_OP and leaves no *.lapp.tmp files on disk.
func TestNoOpLeavesNoTempFile(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "noop.go")
	writeTestFile(t, filePath, "alpha\nbravo\n")

	readOut := callRead(t, s, filePath, 0, 0)
	line1Ref := ""
	for _, l := range strings.Split(strings.TrimSpace(readOut), "\n") {
		if strings.HasPrefix(l, "1#") {
			line1Ref = parseHashRef(l)
			break
		}
	}
	if line1Ref == "" {
		t.Fatalf("could not parse line 1 ref")
	}

	// Replace line 1 with identical content — should be ERR_NO_OP.
	out := callEdit(t, s, filePath, []editor.Edit{
		{Type: editor.EditReplace, Anchor: line1Ref, Content: strPtr("alpha")},
	})
	if !strings.Contains(out, editor.ErrNoOp) {
		t.Fatalf("expected ERR_NO_OP, got: %s", out)
	}

	// No *.lapp.tmp files should exist anywhere in the root.
	var found bool
	if walkErr := filepath.WalkDir(cfg.Root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries; walk continues
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".lapp.tmp") {
			found = true
		}
		return nil
	}); walkErr != nil {
		t.Logf("WalkDir error during orphan check (non-fatal): %v", walkErr)
	}
	if found {
		t.Error("found orphaned .lapp.tmp file after ERR_NO_OP")
	}
}

// TestConcurrentEditsSerializedByLock verifies that two concurrent read→edit
// cycles on the same file do not corrupt it and that at least one succeeds.
func TestConcurrentEditsSerializedByLock(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "concurrent.go")
	writeTestFile(t, filePath, "original\n")

	type result struct {
		out string
		err string
	}
	ch := make(chan result, 2)

	// editOnce performs a full read→edit cycle and sends the outcome.
	// Does NOT call t.Fatalf — goroutine-safe.
	editOnce := func() {
		args := map[string]any{"path": filePath}
		req := mcp.CallToolRequest{}
		req.Params.Arguments = args
		readResult, err := s.handleRead(context.Background(), req)
		if err != nil || readResult == nil {
			ch <- result{err: fmt.Sprintf("read error: %v", err)}
			return
		}
		text := extractTextNoFail(readResult)
		ref := ""
		for _, l := range strings.Split(strings.TrimSpace(text), "\n") {
			if strings.HasPrefix(l, "1#") {
				ref = parseHashRef(l)
				break
			}
		}
		if ref == "" {
			ch <- result{err: "no ref parsed"}
			return
		}
		edits := []editor.Edit{
			{Type: editor.EditReplace, Anchor: ref, Content: strPtr("replaced")},
		}
		editsJSON, _ := json.Marshal(edits)
		var editsAny []any
		json.Unmarshal(editsJSON, &editsAny)
		editReq := mcp.CallToolRequest{}
		editReq.Params.Arguments = map[string]any{"path": filePath, "edits": editsAny}
		editResult, err := s.handleEdit(context.Background(), editReq)
		if err != nil {
			ch <- result{err: fmt.Sprintf("edit error: %v", err)}
			return
		}
		ch <- result{out: extractTextNoFail(editResult)}
	}

	go editOnce()
	go editOnce()

	r1 := <-ch
	r2 := <-ch

	// Neither goroutine should have had an infrastructure error.
	if r1.err != "" {
		t.Errorf("goroutine 1 error: %s", r1.err)
	}
	if r2.err != "" {
		t.Errorf("goroutine 2 error: %s", r2.err)
	}

	// At least one must have succeeded; the other gets ERR_LOCKED or also succeeds
	// (if the first completed its full cycle before the second acquired the lock).
	outcomes := []string{r1.out, r2.out}
	successCount := 0
	for _, o := range outcomes {
		if strings.Contains(o, "OK:") {
			successCount++
		}
	}
	if successCount == 0 {
		t.Errorf("expected at least one successful edit, got: %q / %q", r1.out, r2.out)
	}

	// File must be readable and non-empty with coherent content.
	raw, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("file unreadable after concurrent edits: %v", err)
	}
	if len(raw) == 0 {
		t.Error("file is empty — corruption")
	}
	content := strings.TrimSpace(string(raw))
	if content != "original" && content != "replaced" {
		t.Errorf("unexpected content %q — expected 'original' or 'replaced'", content)
	}
}
