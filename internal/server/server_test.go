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

// parseHashRef extracts the first "N#XX" ref from a formatted line like "N#XX:content".
func parseHashRef(line string) string {
	re := regexp.MustCompile(`(\d+#[A-Z]{2})`)
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
