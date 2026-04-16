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

func callGrepLiteral(t *testing.T, s *Server, pattern, searchPath string) string {
	t.Helper()
	args := map[string]any{"pattern": pattern, "literal": true}
	if searchPath != "" {
		args["path"] = searchPath
	}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	result, err := s.handleGrep(context.Background(), req)
	if err != nil {
		t.Fatalf("handleGrep (literal) error: %v", err)
	}
	return extractText(t, result)
}


func callGrepStructured(t *testing.T, s *Server, pattern, searchPath string) string {
	t.Helper()
	args := map[string]any{"pattern": pattern, "format": "structured"}
	if searchPath != "" {
		args["path"] = searchPath
	}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	result, err := s.handleGrep(context.Background(), req)
	if err != nil {
		t.Fatalf("handleGrep (structured) error: %v", err)
	}
	return extractText(t, result)
}

type parsedStructuredGrep struct {
	Matches []struct {
		Path          string   `json:"path"`
		Anchor        string   `json:"anchor"`
		LineNumber    int      `json:"line_number"`
		Line          string   `json:"line"`
		ContextBefore []string `json:"context_before"`
		ContextAfter  []string `json:"context_after"`
	} `json:"matches"`
}

func callFindBlock(t *testing.T, s *Server, filePath, content string) string {
	t.Helper()
	args := map[string]any{"path": filePath, "content": content, "literal": true}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	result, err := s.handleFindBlock(context.Background(), req)
	if err != nil {
		t.Fatalf("handleFindBlock error: %v", err)
	}
	return extractText(t, result)
}

func callFindBlockNormalized(t *testing.T, s *Server, filePath, content string) string {
	t.Helper()
	args := map[string]any{"path": filePath, "content": content, "literal": true, "normalize_whitespace": true}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	result, err := s.handleFindBlock(context.Background(), req)
	if err != nil {
		t.Fatalf("handleFindBlock (normalized) error: %v", err)
	}
	return extractText(t, result)
}

func callReplaceBlock(t *testing.T, s *Server, filePath, oldContent, newContent string) string {
	t.Helper()
	args := map[string]any{"path": filePath, "old_content": oldContent, "new_content": newContent}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	result, err := s.handleReplaceBlock(context.Background(), req)
	if err != nil {
		t.Fatalf("handleReplaceBlock error: %v", err)
	}
	return extractText(t, result)
}

func callInsertBlock(t *testing.T, s *Server, filePath, anchorContent, newContent, position string) string {
	t.Helper()
	args := map[string]any{
		"path":           filePath,
		"anchor_content": anchorContent,
		"new_content":    newContent,
		"position":       position,
	}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	result, err := s.handleInsertBlock(context.Background(), req)
	if err != nil {
		t.Fatalf("handleInsertBlock error: %v", err)
	}
	return extractText(t, result)
}

func callApplyPatch(t *testing.T, s *Server, filePath, patch string) string {
	t.Helper()
	args := map[string]any{"path": filePath, "patch": patch}
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	result, err := s.handleApplyPatch(context.Background(), req)
	if err != nil {
		t.Fatalf("handleApplyPatch error: %v", err)
	}
	return extractText(t, result)
}

type parsedOperationSuccess struct {
	Status       string `json:"status"`
	Message      string `json:"message"`
	Path         string `json:"path"`
	LinesChanged int    `json:"lines_changed"`
	Diff         string `json:"diff"`
	Warnings     []struct {
		Status  string `json:"status"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"warnings"`
}


type parsedFindBlock struct {
	Matches []struct {
		Path    string   `json:"path"`
		Start   string   `json:"start"`
		End     string   `json:"end"`
		Preview []string `json:"preview"`
	} `json:"matches"`
}

// TestGrepLiteralMatchesRegexMetachars verifies that literal=true treats the
// pattern as a fixed string so code containing regex metacharacters like
// \S+, (?:...), and @? is found correctly — the exact failure seen in the
// benchmark when searching for a URL validator regex.
func TestToolEnabled_DefaultAllowsAll(t *testing.T) {
	s := New(&fileio.Config{Root: t.TempDir()})
	for _, name := range []string{"lapp_read", "lapp_edit", "lapp_apply_patch"} {
		if !s.toolEnabled(name) {
			t.Fatalf("expected tool %s to be enabled by default", name)
		}
	}
}

func TestToolEnabled_OnlyListRestrictsSurface(t *testing.T) {
	s := New(&fileio.Config{Root: t.TempDir(), EnabledTools: []string{"lapp_read", "lapp_apply_patch"}})
	if !s.toolEnabled("lapp_read") || !s.toolEnabled("lapp_apply_patch") {
		t.Fatal("expected configured tools to be enabled")
	}
	for _, name := range []string{"lapp_edit", "lapp_grep", "lapp_replace_block"} {
		if s.toolEnabled(name) {
			t.Fatalf("expected tool %s to be disabled by only-tools filter", name)
		}
	}
}


func TestGrepLiteralMatchesRegexMetachars(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)

	content := "package p\n" +
		"const pat = `(?:\\S+(?::\\S*)?@)?`  // user:pass auth\n" +
		"var x = 1\n"
	filePath := filepath.Join(cfg.Root, "validators.go")
	writeTestFile(t, filePath, content)

	// Literal mode must find the exact line.
	litOut := callGrepLiteral(t, s, `(?:\S+(?::\S*)?@)?`, filePath)
	if !strings.Contains(litOut, "user:pass auth") {
		t.Errorf("literal grep did not match target line:\n%s", litOut)
	}
	// The match must carry a LINE#HASH reference usable in lapp_edit.
	if !strings.Contains(litOut, "#") {
		t.Errorf("literal grep output missing LINE#HASH reference:\n%s", litOut)
	}
}

// TestGrepLiteralInvalidRegexPattern verifies that a string that would be an
// invalid regex is accepted and matched correctly under literal=true.
func TestGrepLiteralInvalidRegexPattern(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)

	content := "line one\nfound [unclosed bracket here\nline three\n"
	filePath := filepath.Join(cfg.Root, "sample.txt")
	writeTestFile(t, filePath, content)

	// Regex mode must return an error.
	regexOut := callGrep(t, s, `[unclosed`, filePath)
	if !strings.Contains(regexOut, "invalid pattern") {
		t.Errorf("expected invalid pattern error in regex mode, got: %s", regexOut)
	}

	// Literal mode must find the line cleanly.
	litOut := callGrepLiteral(t, s, `[unclosed`, filePath)
	if !strings.Contains(litOut, "unclosed bracket here") {
		t.Errorf("literal grep did not find line with invalid-regex text:\n%s", litOut)
	}
}

// TestGrepLiteralMatchesRegexEscapedPunctuation verifies that literal=true
// tolerates model-generated regex escaping on punctuation, e.g. `\[` and `\.`.
// Kimi emitted this shape while searching for a code line under literal=true,
// which caused an unnecessary second grep round trip.
func TestGrepLiteralMatchesRegexEscapedPunctuation(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)

	content := "func f() {\n" +
		"    cright[-right.shape[0]:, -right.shape[1]:] = 1\n" +
		"}\n"
	filePath := filepath.Join(cfg.Root, "separable.py")
	writeTestFile(t, filePath, content)

	// Model over-escaped punctuation as if this were still a regex even though it
	// also set literal=true.
	pat := `cright\[-right\.shape\[0\]:, -right\.shape\[1\]:\] = 1`
	litOut := callGrepLiteral(t, s, pat, filePath)
	if !strings.Contains(litOut, `cright[-right.shape[0]:, -right.shape[1]:] = 1`) {
		t.Errorf("literal grep did not normalize regex-escaped punctuation:\n%s", litOut)
	}
}


func TestGrepStructuredReturnsAnchorAndContext(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "structured.go")
	writeTestFile(t, filePath, "package p\nfunc f() {\n    target()\n}\n")

	out := callGrepStructured(t, s, "target()", filePath)
	var got parsedStructuredGrep
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	if len(got.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d: %s", len(got.Matches), out)
	}
	m := got.Matches[0]
	if m.Anchor == "" || m.LineNumber != 3 || m.Line != "    target()" {
		t.Fatalf("unexpected structured match: %+v", m)
	}
	if len(m.ContextBefore) == 0 || len(m.ContextAfter) == 0 {
		t.Fatalf("expected surrounding context, got: %+v", m)
	}
}

func TestGrepStructuredMultipleMatches(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "multi.go")
	writeTestFile(t, filePath, "a\ntarget\nb\ntarget\nc\n")

	out := callGrepStructured(t, s, "target", filePath)
	var got parsedStructuredGrep
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	if len(got.Matches) != 2 {
		t.Fatalf("expected 2 matches, got %d: %s", len(got.Matches), out)
	}
	if got.Matches[0].LineNumber != 2 || got.Matches[1].LineNumber != 4 {
		t.Fatalf("unexpected line numbers: %+v", got.Matches)
	}
}


func TestFindBlockExactMultilineMatch(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "block.py")
	writeTestFile(t, filePath, "alpha\nbeta\ngamma\ndelta\n")

	out := callFindBlock(t, s, filePath, "beta\ngamma")
	var got parsedFindBlock
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	if len(got.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d: %s", len(got.Matches), out)
	}
	if got.Matches[0].Start == "" || got.Matches[0].End == "" {
		t.Fatalf("missing start/end refs: %s", out)
	}
	if got.Matches[0].Start != "2#QQ" && !strings.HasPrefix(got.Matches[0].Start, "2#") {
		// line number must be correct; hash value may change if hashing implementation changes.
		t.Fatalf("unexpected start ref: %s", got.Matches[0].Start)
	}
	if !strings.Contains(strings.Join(got.Matches[0].Preview, "\n"), "beta") || !strings.Contains(strings.Join(got.Matches[0].Preview, "\n"), "gamma") {
		t.Fatalf("preview missing matched lines: %s", out)
	}
}

func TestFindBlockDisambiguatesRepeatedFirstLine(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "repeat.py")
	writeTestFile(t, filePath, "same\nkeep\nsame\ntarget\nend\n")

	out := callFindBlock(t, s, filePath, "same\ntarget")
	var got parsedFindBlock
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	if len(got.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d: %s", len(got.Matches), out)
	}
	if !strings.HasPrefix(got.Matches[0].Start, "3#") || !strings.HasPrefix(got.Matches[0].End, "4#") {
		t.Fatalf("expected match on lines 3-4, got start=%s end=%s", got.Matches[0].Start, got.Matches[0].End)
	}
}

func TestFindBlockNoMatchReturnsEmptyMatches(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "nomatch.py")
	writeTestFile(t, filePath, "one\ntwo\nthree\n")

	out := callFindBlock(t, s, filePath, "missing\nblock")
	var got parsedFindBlock
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	if len(got.Matches) != 0 {
		t.Fatalf("expected 0 matches, got %d: %s", len(got.Matches), out)
	}
}

func TestFindBlockNormalizeWhitespaceMatchesShiftedIndent(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "indent-block.py")
	writeTestFile(t, filePath, "class A:\n    def f(self):\n        x = 1\n        return x\n")

	query := "            x = 1\n            return x"
	out := callFindBlockNormalized(t, s, filePath, query)
	var got parsedFindBlock
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	if len(got.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d: %s", len(got.Matches), out)
	}
	if !strings.HasPrefix(got.Matches[0].Start, "3#") || !strings.HasPrefix(got.Matches[0].End, "4#") {
		t.Fatalf("expected lines 3-4, got start=%s end=%s", got.Matches[0].Start, got.Matches[0].End)
	}
}

func TestReplaceBlockExactMatch(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "replace-block.py")
	writeTestFile(t, filePath, "alpha\nbeta\ngamma\ndelta\n")

	out := callReplaceBlock(t, s, filePath, "beta\ngamma", "BETA\nGAMMA")
	if !strings.Contains(out, "OK:") {
		t.Fatalf("replace block failed: %s", out)
	}
	data, _ := os.ReadFile(filePath)
	got := string(data)
	if !strings.Contains(got, "BETA\nGAMMA") || strings.Contains(got, "beta") || strings.Contains(got, "gamma") {
		t.Fatalf("file not updated correctly: %s", got)
	}
}

func TestReplaceBlockNormalizeWhitespace(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "replace-block-indent.py")
	writeTestFile(t, filePath, "class A:\n    def f(self):\n        x = 1\n        return x\n")

	old := "            x = 1\n            return x"
	new := "            if cond:\n                x = right"
	out := callReplaceBlock(t, s, filePath, old, new)
	if !strings.Contains(out, "OK:") {
		t.Fatalf("replace block with normalized whitespace failed: %s", out)
	}
	data, _ := os.ReadFile(filePath)
	got := string(data)
	if !strings.Contains(got, "        if cond:\n            x = right") {
		t.Fatalf("expected normalized replace to preserve base + relative indentation, got: %s", got)
	}
}

func TestReplaceBlockAmbiguousMatchErrors(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "replace-ambiguous.py")
	writeTestFile(t, filePath, "same\nblock\nX\nsame\nblock\nY\n")

	out := callReplaceBlock(t, s, filePath, "same\nblock", "new\nblock")
	if !strings.Contains(out, "matched 2 ranges") {
		t.Fatalf("expected ambiguous-match error, got: %s", out)
	}
}


func TestInsertBlockAfterNormalizeWhitespace(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "insert-block-after.py")
	writeTestFile(t, filePath, "class A:\n    def f(self):\n        axis_key = sub[axis]\n        # Axis limits\n        if axis_key in p._limits:\n            pass\n")

	anchor := "            axis_key = sub[axis]"
	newBlock := "            axis_obj = getattr(ax, f\"{axis}axis\")\n\n            # Nominal scale special-casing\n            if isinstance(self._scales.get(axis_key), Nominal):\n                axis_obj.grid(False, which=\"both\")"
	out := callInsertBlock(t, s, filePath, anchor, newBlock, "after")
	if !strings.Contains(out, "OK:") {
		t.Fatalf("insert block after failed: %s", out)
	}
	data, _ := os.ReadFile(filePath)
	got := string(data)
	expect := "        axis_obj = getattr(ax, f\"{axis}axis\")\n\n        # Nominal scale special-casing\n        if isinstance(self._scales.get(axis_key), Nominal):\n            axis_obj.grid(False, which=\"both\")"
	if !strings.Contains(got, expect) {
		t.Fatalf("expected normalized inserted block, got: %s", got)
	}
}

func TestInsertBlockBefore(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "insert-block-before.py")
	writeTestFile(t, filePath, "from seaborn._core.subplots import Subplots\nfrom seaborn._core.groupby import GroupBy\n")

	out := callInsertBlock(t, s, filePath, "from seaborn._core.subplots import Subplots", "from seaborn._core.scales import Scale, Nominal", "before")
	if !strings.Contains(out, "OK:") {
		t.Fatalf("insert block before failed: %s", out)
	}
	data, _ := os.ReadFile(filePath)
	got := string(data)
	expect := "from seaborn._core.scales import Scale, Nominal\nfrom seaborn._core.subplots import Subplots"
	if !strings.Contains(got, expect) {
		t.Fatalf("expected inserted import before anchor, got: %s", got)
	}
}

func TestInsertBlockStaleReturnsStructuredRepairPayload(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "insert-stale.txt")
	writeTestFile(t, filePath, "alpha\nbeta\ngamma\ndelta\n")

	writeTestFile(t, filePath, "alpha\nBETA\ngamma\ndelta\n")
	out := callInsertBlock(t, s, filePath, "beta\ngamma", "inserted", "after")
	var payload editor.StaleRefRepairResult
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("expected stale repair JSON, got: %s\nerr: %v", out, err)
	}
	if payload.Status != "stale_refs" || len(payload.Changed) == 0 {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if !strings.Contains(payload.Changed[0].Line, "BETA") {
		t.Fatalf("expected updated anchor line in payload: %+v", payload)
	}
}


func TestApplyPatchExactReplaceAndInsertDelete(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "apply-patch.txt")
	writeTestFile(t, filePath, "import scale\nalpha\nbeta\ngamma\ndelta\nepsilon\nzeta\n")

	patch := strings.Join([]string{
		"--- a/apply-patch.txt",
		"+++ b/apply-patch.txt",
		"@@ -1,2 +1,2 @@",
		"-import scale",
		"+import scale, nominal",
		" alpha",
		"@@ -5,3 +5,4 @@",
		" delta",
		" epsilon",
		"+inserted",
		" zeta",
	}, "\n")

	out := callApplyPatch(t, s, filePath, patch)
	if !strings.Contains(out, "OK:") {
		t.Fatalf("apply patch failed: %s", out)
	}
	data, _ := os.ReadFile(filePath)
	got := string(data)
	if !strings.Contains(got, "import scale, nominal") || !strings.Contains(got, "epsilon\ninserted\nzeta") {
		t.Fatalf("file not patched correctly: %s", got)
	}
}

func TestApplyPatchStaleReturnsStructuredRepairPayload(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "apply-patch-stale.txt")
	writeTestFile(t, filePath, "alpha\nbeta\ngamma\n")
	writeTestFile(t, filePath, "alpha\nBETA\ngamma\n")

	patch := strings.Join([]string{
		"--- a/apply-patch-stale.txt",
		"+++ b/apply-patch-stale.txt",
		"@@ -1,3 +1,3 @@",
		" alpha",
		"-beta",
		"+replaced",
		" gamma",
	}, "\n")

	out := callApplyPatch(t, s, filePath, patch)
	var payload editor.StaleRefRepairResult
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("expected stale repair JSON, got: %s\nerr: %v", out, err)
	}
	if payload.Status != "stale_refs" || len(payload.Changed) == 0 {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if !strings.Contains(payload.Changed[0].Line, "BETA") {
		t.Fatalf("expected updated line in stale payload: %+v", payload)
	}
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

// firstLineDisplayRef returns the displayed LINE#HASH: prefix from a formatted
// line like "N#XX:content". Models often copy this exact token into lapp_edit.
func firstLineDisplayRef(t *testing.T, text, contains string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, contains) {
			if i := strings.Index(line, ":"); i != -1 {
				return line[:i+1]
			}
		}
	}
	t.Fatalf("no display ref found in lines containing %q in:\n%s", contains, text)
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


// Models often copy the displayed ref prefix from lapp_read output, e.g.
// "2#KH:" instead of the machine form "2#KH". lapp_edit should accept that
// directly rather than bouncing the model into a retry loop.
func TestRoundTrip_ReadEditAcceptsColonSuffixedAnchor(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)

	filePath := filepath.Join(cfg.Root, "anchor-colon.txt")
	writeTestFile(t, filePath, "line one\nline two\nline three\n")

	readOut := callRead(t, s, filePath, 0, 0)
	anchor := firstLineDisplayRef(t, readOut, "line two") // e.g. "2#KH:"

	editOut := callEdit(t, s, filePath, []editor.Edit{
		{Type: editor.EditReplace, Anchor: anchor, Content: strPtr("line 2")},
	})
	if !strings.Contains(editOut, "OK:") {
		t.Fatalf("edit with colon-suffixed anchor failed: %s", editOut)
	}

	data, _ := os.ReadFile(filePath)
	if got := string(data); !strings.Contains(got, "line 2") || strings.Contains(got, "line two") {
		t.Fatalf("file not updated correctly: %s", got)
	}
}

func TestRoundTrip_ReadEditAcceptsColonSuffixedRangeRefs(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)

	filePath := filepath.Join(cfg.Root, "range-colon.txt")
	writeTestFile(t, filePath, "alpha\nbeta\ngamma\ndelta\n")

	readOut := callRead(t, s, filePath, 0, 0)
	start := firstLineDisplayRef(t, readOut, "beta")
	end := firstLineDisplayRef(t, readOut, "gamma")

	editOut := callEdit(t, s, filePath, []editor.Edit{
		{Type: editor.EditReplace, Start: start, End: end, Content: strPtr("BETA\nGAMMA")},
	})
	if !strings.Contains(editOut, "OK:") {
		t.Fatalf("edit with colon-suffixed range refs failed: %s", editOut)
	}

	data, _ := os.ReadFile(filePath)
	got := string(data)
	if !strings.Contains(got, "BETA\nGAMMA") || strings.Contains(got, "beta") || strings.Contains(got, "gamma") {
		t.Fatalf("file not updated correctly: %s", got)
	}
}


func TestRoundTrip_ReadEditAcceptsFullDisplayLineAnchor(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "anchor-displayline.txt")
	writeTestFile(t, filePath, "line one\nline two\nline three\n")

	readOut := callRead(t, s, filePath, 0, 0)
	anchor := ""
	for _, line := range strings.Split(readOut, "\n") {
		if strings.Contains(line, "line two") {
			anchor = line // full LINE#HASH:CONTENT line
			break
		}
	}
	if anchor == "" {
		t.Fatalf("failed to capture display line from read output: %s", readOut)
	}

	editOut := callEdit(t, s, filePath, []editor.Edit{{Type: editor.EditReplace, Anchor: anchor, Content: strPtr("line 2")}})
	if !strings.Contains(editOut, "OK:") {
		t.Fatalf("edit with full display-line anchor failed: %s", editOut)
	}
	data, _ := os.ReadFile(filePath)
	if got := string(data); !strings.Contains(got, "line 2") || strings.Contains(got, "line two") {
		t.Fatalf("file not updated correctly: %s", got)
	}
}

func TestRoundTrip_ReadEditAcceptsColonSeparatedHashRef(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "anchor-colonhash.txt")
	writeTestFile(t, filePath, "line one\nline two\nline three\n")

	readOut := callRead(t, s, filePath, 0, 0)
	ref := firstLineRef(t, readOut, "line two")
	colonRef := strings.Replace(ref, "#", ":", 1)

	editOut := callEdit(t, s, filePath, []editor.Edit{{Type: editor.EditReplace, Anchor: colonRef, Content: strPtr("line 2")}})
	if !strings.Contains(editOut, "OK:") {
		t.Fatalf("edit with colon-separated hash ref failed: %s", editOut)
	}
	data, _ := os.ReadFile(filePath)
	if got := string(data); !strings.Contains(got, "line 2") || strings.Contains(got, "line two") {
		t.Fatalf("file not updated correctly: %s", got)
	}
}

func TestRoundTrip_ReadEditAcceptsFullDisplayLineRangeRefs(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "range-displayline.txt")
	writeTestFile(t, filePath, "alpha\nbeta\ngamma\ndelta\n")

	readOut := callRead(t, s, filePath, 0, 0)
	var start, end string
	for _, line := range strings.Split(readOut, "\n") {
		if strings.Contains(line, "beta") {
			start = line
		}
		if strings.Contains(line, "gamma") {
			end = line
		}
	}
	if start == "" || end == "" {
		t.Fatalf("failed to capture display lines from read output: %s", readOut)
	}

	editOut := callEdit(t, s, filePath, []editor.Edit{{Type: editor.EditReplace, Start: start, End: end, Content: strPtr("BETA\nGAMMA")}})
	if !strings.Contains(editOut, "OK:") {
		t.Fatalf("edit with full display-line range refs failed: %s", editOut)
	}
	data, _ := os.ReadFile(filePath)
	got := string(data)
	if !strings.Contains(got, "BETA\nGAMMA") || strings.Contains(got, "beta") || strings.Contains(got, "gamma") {
		t.Fatalf("file not updated correctly: %s", got)
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

	// Attempt edit with stale ref — expect structured stale-ref payload.
	editOut := callEdit(t, s, filePath, []editor.Edit{
		{Type: editor.EditReplace, Anchor: staleRef, Content: strPtr("replaced")},
	})
	var payload editor.StaleRefRepairResult
	if err := json.Unmarshal([]byte(editOut), &payload); err != nil {
		t.Fatalf("expected stale repair JSON, got: %s\nerr: %v", editOut, err)
	}
	if payload.Status != "stale_refs" || payload.ErrorCode != editor.ErrHashMismatch {
		t.Fatalf("unexpected stale payload: %+v", payload)
	}
	if len(payload.Changed) == 0 {
		t.Fatalf("missing changed anchors in payload: %+v", payload)
	}
	updatedRef := payload.Changed[0].Anchor

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

func TestRepeatedStaleRetryStrengthensMessage(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "stale-repeat.txt")
	writeTestFile(t, filePath, "alpha\nbeta\ngamma\n")

	readOut := callRead(t, s, filePath, 0, 0)
	staleRef := firstLineRef(t, readOut, "beta")
	writeTestFile(t, filePath, "alpha\nBETA\ngamma\n")

	firstOut := callEdit(t, s, filePath, []editor.Edit{{Type: editor.EditReplace, Anchor: staleRef, Content: strPtr("replaced")}})
	var first editor.StaleRefRepairResult
	if err := json.Unmarshal([]byte(firstOut), &first); err != nil {
		t.Fatalf("expected stale repair JSON on first retry, got: %s\nerr: %v", firstOut, err)
	}
	secondOut := callEdit(t, s, filePath, []editor.Edit{{Type: editor.EditReplace, Anchor: staleRef, Content: strPtr("replaced")}})
	var second editor.StaleRefRepairResult
	if err := json.Unmarshal([]byte(secondOut), &second); err != nil {
		t.Fatalf("expected stale repair JSON on second retry, got: %s\nerr: %v", secondOut, err)
	}
	if !strings.Contains(second.Message, "repeatedly") {
		t.Fatalf("expected escalated stale message, got: %+v", second)
	}
	if !strings.Contains(second.Note, "already have fresh anchors") {
		t.Fatalf("expected direct-anchor guidance, got: %+v", second)
	}
	_ = first
}

func TestRepeatedSearchThenEditAddsWarningPrefix(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "search-warning.txt")
	writeTestFile(t, filePath, "alpha\nbeta\ngamma\n")

	_ = callRead(t, s, filePath, 0, 0)
	_ = callRead(t, s, filePath, 1, 2)
	_ = callGrepLiteral(t, s, "beta", filePath)
	_ = callGrepLiteral(t, s, "gamma", filePath)

	readOut := callRead(t, s, filePath, 0, 0)
	ref := firstLineRef(t, readOut, "beta")
	out := callEdit(t, s, filePath, []editor.Edit{{Type: editor.EditReplace, Anchor: ref, Content: strPtr("BETA")}})
	var resp parsedOperationSuccess
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("expected JSON success envelope, got: %s\nerr: %v", out, err)
	}
	if resp.Status != "ok" || len(resp.Warnings) == 0 || resp.Warnings[0].Code != "REPEATED_SEARCH_RECOMMENDATION" {
		t.Fatalf("expected repeated search warning, got: %+v", resp)
	}
	if !strings.Contains(resp.Message, "OK:") {
		t.Fatalf("expected OK message in envelope, got: %+v", resp)
	}
	data, _ := os.ReadFile(filePath)
	if !strings.Contains(string(data), "BETA") {
		t.Fatalf("file not updated correctly: %s", string(data))
	}
}


func TestRepeatedFindBlockThenEditAddsWarningPrefix(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "multiline-warning.txt")
	writeTestFile(t, filePath, "alpha\nbeta\ngamma\ndelta\n")

	_ = callFindBlock(t, s, filePath, "beta\ngamma")
	blockOut := callFindBlock(t, s, filePath, "beta\ngamma")
	var parsed parsedFindBlock
	if err := json.Unmarshal([]byte(blockOut), &parsed); err != nil {
		t.Fatalf("expected find block JSON, got: %s\nerr: %v", blockOut, err)
	}
	if len(parsed.Matches) != 1 {
		t.Fatalf("expected one block match, got: %+v", parsed)
	}
	out := callEdit(t, s, filePath, []editor.Edit{{Type: editor.EditReplace, Start: parsed.Matches[0].Start, End: parsed.Matches[0].End, Content: strPtr("BETA\nGAMMA")}})
	var resp parsedOperationSuccess
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("expected JSON success envelope, got: %s\nerr: %v", out, err)
	}
	if resp.Status != "ok" || len(resp.Warnings) == 0 || resp.Warnings[0].Code != "MULTILINE_HELPER_RECOMMENDATION" {
		t.Fatalf("expected multiline helper warning, got: %+v", resp)
	}
	data, _ := os.ReadFile(filePath)
	if !strings.Contains(string(data), "BETA\nGAMMA") {
		t.Fatalf("file not updated correctly: %s", string(data))
	}
}


func TestRepeatedSearchThenReplaceBlockAddsWarningPrefix(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "replace-search-warning.txt")
	writeTestFile(t, filePath, "alpha\nbeta\ngamma\ndelta\n")

	_ = callRead(t, s, filePath, 0, 0)
	_ = callRead(t, s, filePath, 1, 2)
	_ = callGrepLiteral(t, s, "beta", filePath)
	_ = callGrepLiteral(t, s, "gamma", filePath)

	out := callReplaceBlock(t, s, filePath, "beta\ngamma", "BETA\nGAMMA")
	var resp parsedOperationSuccess
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("expected JSON success envelope, got: %s\nerr: %v", out, err)
	}
	if resp.Status != "ok" || len(resp.Warnings) == 0 || resp.Warnings[0].Code != "REPEATED_SEARCH_RECOMMENDATION" {
		t.Fatalf("expected repeated search warning on replace_block, got: %+v", resp)
	}
}

func TestRepeatedSearchThenInsertBlockAddsWarningPrefix(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "insert-search-warning.txt")
	writeTestFile(t, filePath, "one\ntwo\nthree\n")

	_ = callRead(t, s, filePath, 0, 0)
	_ = callRead(t, s, filePath, 1, 2)
	_ = callGrepLiteral(t, s, "two", filePath)
	_ = callGrepLiteral(t, s, "three", filePath)

	out := callInsertBlock(t, s, filePath, "two", "inserted", "after")
	var resp parsedOperationSuccess
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("expected JSON success envelope, got: %s\nerr: %v", out, err)
	}
	if resp.Status != "ok" || len(resp.Warnings) == 0 || resp.Warnings[0].Code != "REPEATED_SEARCH_RECOMMENDATION" {
		t.Fatalf("expected repeated search warning on insert_block, got: %+v", resp)
	}
}

func TestRepeatedSearchThenApplyPatchAddsWarningPrefix(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "patch-search-warning.txt")
	writeTestFile(t, filePath, "alpha\nbeta\ngamma\n")

	_ = callRead(t, s, filePath, 0, 0)
	_ = callRead(t, s, filePath, 1, 2)
	_ = callGrepLiteral(t, s, "beta", filePath)
	_ = callGrepLiteral(t, s, "gamma", filePath)
	patch := strings.Join([]string{
		"--- a/patch-search-warning.txt",
		"+++ b/patch-search-warning.txt",
		"@@ -1,3 +1,3 @@",
		" alpha",
		"-beta",
		"+BETA",
		" gamma",
	}, "\n")

	out := callApplyPatch(t, s, filePath, patch)
	var resp parsedOperationSuccess
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("expected JSON success envelope, got: %s\nerr: %v", out, err)
	}
	if resp.Status != "ok" || len(resp.Warnings) == 0 || resp.Warnings[0].Code != "REPEATED_SEARCH_RECOMMENDATION" {
		t.Fatalf("expected repeated search warning on apply_patch, got: %+v", resp)
	}
}
func TestRepeatedStaleReplaceBlockStrengthensMessage(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "replace-stale-repeat.txt")
	writeTestFile(t, filePath, "alpha\nbeta\ngamma\n")
	writeTestFile(t, filePath, "alpha\nBETA\ngamma\n")

	firstOut := callReplaceBlock(t, s, filePath, "beta\ngamma", "BETA\nGAMMA")
	var first editor.StaleRefRepairResult
	if err := json.Unmarshal([]byte(firstOut), &first); err != nil {
		t.Fatalf("expected stale repair JSON on first replace retry, got: %s\nerr: %v", firstOut, err)
	}
	secondOut := callReplaceBlock(t, s, filePath, "beta\ngamma", "BETA\nGAMMA")
	var second editor.StaleRefRepairResult
	if err := json.Unmarshal([]byte(secondOut), &second); err != nil {
		t.Fatalf("expected stale repair JSON on second replace retry, got: %s\nerr: %v", secondOut, err)
	}
	if !strings.Contains(second.Message, "repeatedly") || !strings.Contains(second.Note, "already have fresh anchors") {
		t.Fatalf("expected escalated stale message for replace_block, got: %+v", second)
	}
	_ = first
}

func TestRepeatedStaleApplyPatchStrengthensMessage(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "patch-stale-repeat.txt")
	writeTestFile(t, filePath, "alpha\nbeta\ngamma\n")
	writeTestFile(t, filePath, "alpha\nBETA\ngamma\n")
	patch := strings.Join([]string{
		"--- a/patch-stale-repeat.txt",
		"+++ b/patch-stale-repeat.txt",
		"@@ -1,3 +1,3 @@",
		" alpha",
		"-beta",
		"+replaced",
		" gamma",
	}, "\n")

	firstOut := callApplyPatch(t, s, filePath, patch)
	var first editor.StaleRefRepairResult
	if err := json.Unmarshal([]byte(firstOut), &first); err != nil {
		t.Fatalf("expected stale repair JSON on first apply_patch retry, got: %s\nerr: %v", firstOut, err)
	}
	secondOut := callApplyPatch(t, s, filePath, patch)
	var second editor.StaleRefRepairResult
	if err := json.Unmarshal([]byte(secondOut), &second); err != nil {
		t.Fatalf("expected stale repair JSON on second apply_patch retry, got: %s\nerr: %v", secondOut, err)
	}
	if !strings.Contains(second.Message, "repeatedly") || !strings.Contains(second.Note, "already have fresh anchors") {
		t.Fatalf("expected escalated stale message for apply_patch, got: %+v", second)
	}
	_ = first
}


func TestRoundTrip_SelfCorrectMissingHashMessage(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)

	filePath := filepath.Join(cfg.Root, "sc-missing-hash.txt")
	writeTestFile(t, filePath, "foo\nbar\nbaz\n")

	editOut := callEdit(t, s, filePath, []editor.Edit{
		{Type: editor.EditReplace, Anchor: "245", Content: strPtr("quux")},
	})

	var sc editor.SelfCorrectResult
	if err := json.Unmarshal([]byte(editOut), &sc); err != nil {
		t.Fatalf("expected SelfCorrectResult JSON, got: %s\nerr: %v", editOut, err)
	}
	if !strings.Contains(sc.Message, "missing the #HASH part") {
		t.Fatalf("expected missing-hash guidance, got: %s", sc.Message)
	}
}

func TestRoundTrip_StaleRefReturnsStructuredRepairPayload(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "stale-structured.txt")
	writeTestFile(t, filePath, "alpha\nbeta\ngamma\n")

	readOut := callRead(t, s, filePath, 0, 0)
	staleRef := firstLineRef(t, readOut, "beta")
	writeTestFile(t, filePath, "alpha\nBETA\ngamma\n")

	editOut := callEdit(t, s, filePath, []editor.Edit{{Type: editor.EditReplace, Anchor: staleRef, Content: strPtr("replaced")}})
	var payload editor.StaleRefRepairResult
	if err := json.Unmarshal([]byte(editOut), &payload); err != nil {
		t.Fatalf("expected stale repair JSON, got: %s\nerr: %v", editOut, err)
	}
	if payload.Status != "stale_refs" || payload.ErrorCode != editor.ErrHashMismatch {
		t.Fatalf("unexpected stale payload: %+v", payload)
	}
	if len(payload.Changed) == 0 || payload.Changed[0].Anchor == "" {
		t.Fatalf("missing changed anchors in payload: %+v", payload)
	}
	if !strings.Contains(payload.Changed[0].Line, "BETA") {
		t.Fatalf("expected updated line content in payload: %+v", payload)
	}
}

func TestReplaceBlockStaleReturnsStructuredRepairPayload(t *testing.T) {
	cfg := newTestConfig(t)
	s := New(cfg)
	filePath := filepath.Join(cfg.Root, "replace-stale.py")
	writeTestFile(t, filePath, "class A:\n    def f(self):\n        x = 1\n        return x\n")

	oldBlock := "        x = 1\n        return x"
	writeTestFile(t, filePath, "class A:\n    def f(self):\n        x = 2\n        return x\n")

	out := callReplaceBlock(t, s, filePath, oldBlock, "        x = right\n        return x")
	var payload editor.StaleRefRepairResult
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("expected stale repair JSON, got: %s\nerr: %v", out, err)
	}
	if payload.Status != "stale_refs" || len(payload.Changed) == 0 {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if !strings.Contains(payload.Changed[0].Line, "x = 2") {
		t.Fatalf("expected changed block line in payload: %+v", payload)
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
