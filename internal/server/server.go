package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/lapp-dev/lapp/internal/editor"
	"github.com/lapp-dev/lapp/internal/fileio"
	"github.com/lapp-dev/lapp/pkg/hashline"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

const version = "0.1.0"

// Server wraps an MCPServer with project configuration.
type Server struct {
	cfg   *fileio.Config
	mcpS  *mcpserver.MCPServer
	mu    sync.Mutex
	guard map[string]*fileGuardState
}

type fileGuardState struct {
	recentFindBlocks int
	recentSearchOps  int
	lastAnchorSig    string
	staleCount       int
}
type operationWarning struct {
	Status  string `json:"status"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type operationSuccess struct {
	Status       string             `json:"status"`
	Message      string             `json:"message"`
	Path         string             `json:"path,omitempty"`
	LinesChanged int                `json:"lines_changed,omitempty"`
	Diff         string             `json:"diff,omitempty"`
	Warnings     []operationWarning `json:"warnings,omitempty"`
}

// New creates and configures an MCP server. Emits a CLAUDE.md hint to stderr.
func New(cfg *fileio.Config) *Server {
	fmt.Fprintln(os.Stderr, "lapp: add to CLAUDE.md → Prefer lapp_read/lapp_edit/lapp_write/lapp_grep over built-in Read/Edit/Write/Grep; for multiline helpers prefer lapp_insert_block, lapp_replace_block, and lapp_apply_patch when one file has multiple hunks")

	s := &Server{cfg: cfg, guard: map[string]*fileGuardState{}}
	s.mcpS = mcpserver.NewMCPServer("lapp", version, mcpserver.WithToolCapabilities(false))
	s.registerTools()
	return s
}

// Start begins serving MCP over stdio.
func (s *Server) Start() error {
	return mcpserver.ServeStdio(s.mcpS)
}

func (s *Server) stateFor(path string) *fileGuardState {
	st := s.guard[path]
	if st == nil {
		st = &fileGuardState{}
		s.guard[path] = st
	}
	return st
}

func anchorsSignature(changed []editor.StaleRefRepairLine) string {
	parts := make([]string, 0, len(changed))
	for _, ch := range changed {
		parts = append(parts, ch.Anchor)
	}
	return strings.Join(parts, "|")
}

func (s *Server) applyStaleRetryGuard(path string, payload *editor.StaleRefRepairResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stateFor(path)
	sig := anchorsSignature(payload.Changed)
	if sig != "" && sig == st.lastAnchorSig {
		st.staleCount++
	} else {
		st.lastAnchorSig = sig
		st.staleCount = 1
	}
	if st.staleCount >= 2 {
		payload.Message = "This file returned stale_refs repeatedly for the same local region. Retry using the fresh anchors below instead of rereading."
		payload.Note = "You already have fresh anchors for this region. Reuse them directly in your retry instead of running another broad read or grep."
	}
}

func (s *Server) recordFindBlock(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stateFor(path)
	st.recentFindBlocks++
}

func (s *Server) recordSearchOp(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stateFor(path)
	st.recentSearchOps++
}

func (s *Server) consumeSearchWarning(path string) *operationWarning {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stateFor(path)
	warn := st.recentSearchOps >= 4
	st.recentSearchOps = 0
	if !warn {
		return nil
	}
	return &operationWarning{
		Status:  "warning",
		Code:    "REPEATED_SEARCH_RECOMMENDATION",
		Message: "You searched or reread this file repeatedly before editing. Prefer a more direct helper such as lapp_replace_block, lapp_insert_block, or lapp_apply_patch when applicable.",
	}
}

func (s *Server) consumeMultilineWarning(path string, req *editor.EditRequest) *operationWarning {
	hasRangeReplace := false
	for _, e := range req.Edits {
		if e.Type == editor.EditReplace && e.Start != "" && e.End != "" {
			hasRangeReplace = true
			break
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stateFor(path)
	warn := hasRangeReplace && st.recentFindBlocks >= 2
	st.recentFindBlocks = 0
	if !warn {
		return nil
	}
	return &operationWarning{
		Status:  "warning",
		Code:    "MULTILINE_HELPER_RECOMMENDATION",
		Message: "You used lapp_find_block plus lapp_edit repeatedly on the same file for multiline edits. Prefer lapp_replace_block for exact block replacements.",
	}
}

func (s *Server) resetMultilineTracking(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stateFor(path)
	st.recentFindBlocks = 0
}

func marshalGuardedStalePayload(payload editor.StaleRefRepairResult, apply func(*editor.StaleRefRepairResult)) string {
	if apply != nil {
		apply(&payload)
	}
	jsonBytes, _ := json.Marshal(payload)
	return string(jsonBytes)
}

func marshalSuccessResponse(message, path string, result *editor.EditResult, warnings ...*operationWarning) string {
	resp := operationSuccess{Status: "ok", Message: message, Path: path}
	if result != nil {
		resp.LinesChanged = result.LinesChanged
		resp.Diff = result.Diff
	}
	for _, w := range warnings {
		if w != nil {
			resp.Warnings = append(resp.Warnings, *w)
		}
	}
	jsonBytes, _ := json.Marshal(resp)
	return string(jsonBytes)
}

func (s *Server) toolEnabled(name string) bool {
	if s.cfg == nil || len(s.cfg.EnabledTools) == 0 {
		return true
	}
	for _, allowed := range s.cfg.EnabledTools {
		if strings.TrimSpace(allowed) == name {
			return true
		}
	}
	return false
}

func (s *Server) registerTools() {
	// lapp_read
	readTool := mcp.NewTool("lapp_read",
		mcp.WithDescription(`Read a file with content-hash-tagged line references. Each line is prefixed with LINE#HASH: where LINE is the line number and HASH is a 2-character content fingerprint. Hash references from any page remain valid for the entire file — you may paginate with offset/limit and combine references across pages. Use lapp_edit to modify files read with this tool.`),
		mcp.WithString("path", mcp.Required(), mcp.Description("Absolute path to the file (must be within the configured root)")),
		mcp.WithNumber("offset", mcp.Description("Start line, 1-indexed (default: 1)")),
		mcp.WithNumber("limit", mcp.Description("Max lines to return (default: server default, typically 2000)")),
	)
	if s.toolEnabled("lapp_read") {
		s.mcpS.AddTool(readTool, s.handleRead)
	}
	// lapp_edit — raw schema because mcp-go builder can't express nested enum within items.
	editsRawSchema := `{
		"type": "object",
		"required": ["path", "edits"],
		"properties": {
			"path": {"type": "string", "description": "Absolute path to the file"},
			"edits": {
				"type": "array",
				"maxItems": 100,
				"items": {
					"type": "object",
					"required": ["type"],
					"properties": {
						"type": {"type": "string", "enum": ["replace", "insert_after", "insert_before", "delete"]},
						"anchor": {"type": "string", "description": "LINE#HASH reference for single-line ops. Special: \"0:\" = beginning of file, \"EOF:\" = end of file"},
						"start":  {"type": "string", "description": "LINE#HASH of first line in range"},
						"end":    {"type": "string", "description": "LINE#HASH of last line in range"},
						"content": {"type": "string", "description": "Replacement/insertion content. Multiple lines separated by \\n. Required for replace/insert ops; absent for delete."}
					}
				}
			}
		}
	}`
	editTool := mcp.NewToolWithRawSchema("lapp_edit",
		`Edit a file using LINE#HASH references from lapp_read. Addressing: for single-line operations use anchor; for range operations use start and end (never anchor and start/end together). Operations: replace (single line or range), insert_after (single line only), insert_before (single line only), delete (single line or range). All edits are validated atomically — if any reference is stale, nothing is written and updated references are provided. Read the file with lapp_read first. Special anchors: "0:" inserts at beginning, "EOF:" appends at end. Maximum 100 edits per call.`,
		json.RawMessage(editsRawSchema),
	)
	if s.toolEnabled("lapp_edit") {
		s.mcpS.AddTool(editTool, s.handleEdit)
	}

	// lapp_write
	writeTool := mcp.NewTool("lapp_write",
		mcp.WithDescription(`Create a new file with the given content. For new files only — returns an error if the file already exists. To modify an existing file, use lapp_read + lapp_edit instead, which is faster and safer. Parent directories are created automatically.`),
		mcp.WithString("path", mcp.Required(), mcp.Description("Absolute path to the new file")),
		mcp.WithString("content", mcp.Required(), mcp.Description("Complete file content")),
	)
	if s.toolEnabled("lapp_write") {
		s.mcpS.AddTool(writeTool, s.handleWrite)
	}

	// lapp_grep
	grepTool := mcp.NewTool("lapp_grep",
		mcp.WithDescription(`Search files for a pattern and return matches with LINE#HASH references. Use the returned references directly in lapp_edit without a separate lapp_read call. Use literal=true when searching for code that contains regex special characters such as ( ) \ . + * ? [ ] ^ $ |. Use format=structured for machine-readable anchors, line text, and context.`),
		mcp.WithString("pattern", mcp.Required(), mcp.Description("Pattern to search for. Interpreted as a regex unless literal=true")),
		mcp.WithString("path", mcp.Description("File or directory to search (defaults to root)")),
		mcp.WithNumber("context", mcp.Description("Lines of context around matches (default: 2)")),
		mcp.WithBoolean("literal", mcp.Description("If true, treat pattern as a fixed string (no regex interpretation). Use when the search term contains code or regex metacharacters")),
		mcp.WithString("format", mcp.Description("Output format: text (default) or structured")),
	)
	if s.toolEnabled("lapp_grep") {
		s.mcpS.AddTool(grepTool, s.handleGrep)
	}

	// lapp_find_block
	findBlockTool := mcp.NewTool("lapp_find_block",
		mcp.WithDescription(`Find an exact multi-line code block in a file and return start/end LINE#HASH references usable directly in lapp_edit. Use this for multi-line replacements where grepping the first and last line separately is ambiguous. By default, matching ignores shared leading indentation differences across the whole block.`),
		mcp.WithString("path", mcp.Required(), mcp.Description("Absolute path to the file to search")),
		mcp.WithString("content", mcp.Required(), mcp.Description("Exact block content to find. Preserve line breaks exactly.")),
		mcp.WithBoolean("literal", mcp.Description("If true, treat content as a literal block (default true). Regex block search is not currently supported.")),
		mcp.WithBoolean("normalize_whitespace", mcp.Description("If false, require exact indentation match. Default true ignores shared leading indentation differences when matching the block.")),
	)
	if s.toolEnabled("lapp_find_block") {
		s.mcpS.AddTool(findBlockTool, s.handleFindBlock)
	}

	// lapp_replace_block
	replaceBlockTool := mcp.NewTool("lapp_replace_block",
		mcp.WithDescription(`Replace one exact multi-line block in a file. Provide old_content and new_content; lapp will find the old block, resolve start/end refs internally, and apply one range replacement. By default, matching ignores shared leading indentation differences across the whole block.`),
		mcp.WithString("path", mcp.Required(), mcp.Description("Absolute path to the file to modify")),
		mcp.WithString("old_content", mcp.Required(), mcp.Description("Exact old block to replace.")),
		mcp.WithString("new_content", mcp.Required(), mcp.Description("Replacement block content.")),
		mcp.WithBoolean("normalize_whitespace", mcp.Description("If false, require exact indentation match. Default true ignores shared leading indentation differences when matching the old block.")),
	)
	if s.toolEnabled("lapp_replace_block") {
		s.mcpS.AddTool(replaceBlockTool, s.handleReplaceBlock)
	}

	// lapp_insert_block
	insertBlockTool := mcp.NewTool("lapp_insert_block",
		mcp.WithDescription(`Insert a multi-line block before or after an exact anchor block. Provide anchor_content and new_content; lapp finds the anchor internally, preserves the anchor block's base indentation in the inserted block, and applies one insert edit. Use this for insertion-only multiline changes where manual range edits are error-prone.`),
		mcp.WithString("path", mcp.Required(), mcp.Description("Absolute path to the file to modify")),
		mcp.WithString("anchor_content", mcp.Required(), mcp.Description("Exact anchor block to insert before or after.")),
		mcp.WithString("new_content", mcp.Required(), mcp.Description("Block content to insert.")),
		mcp.WithString("position", mcp.Required(), mcp.Description("Insert position relative to the anchor: before or after.")),
		mcp.WithBoolean("normalize_whitespace", mcp.Description("If false, require exact indentation match. Default true ignores shared leading indentation differences when matching the anchor block and rebases inserted indentation to the anchor block.")),
	)
	if s.toolEnabled("lapp_insert_block") {
		s.mcpS.AddTool(insertBlockTool, s.handleInsertBlock)
	}

	// lapp_apply_patch
	applyPatchTool := mcp.NewTool("lapp_apply_patch",
		mcp.WithDescription(`Apply a single-file unified diff atomically. Provide path and patch; lapp parses the hunks, matches each old block with surrounding context, and applies all replacements in one batch. Use this for repeated edits in one file when manual sequencing is error-prone.`),
		mcp.WithString("path", mcp.Required(), mcp.Description("Absolute path to the file to modify")),
		mcp.WithString("patch", mcp.Required(), mcp.Description("Unified diff for one file (---/+++ headers plus one or more @@ hunks).")),
		mcp.WithBoolean("normalize_whitespace", mcp.Description("If false, require exact indentation match. Default true ignores shared leading indentation differences when matching hunks and rebases replacement indentation to the matched block.")),
	)
	if s.toolEnabled("lapp_apply_patch") {
		s.mcpS.AddTool(applyPatchTool, s.handleApplyPatch)
	}
}

func (s *Server) handleRead(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	path, err := req.RequireString("path")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	canonical, errCode := fileio.CheckPath(path, s.cfg, true)
	if errCode != "" {
		return mcp.NewToolResultError(errCode), nil
	}

	args := req.GetArguments()

	offset := 1
	if v, ok := args["offset"]; ok {
		if f, ok2 := v.(float64); ok2 && f > 0 {
			offset = int(f)
		}
	}

	limit := s.cfg.DefaultLimit
	if v, ok := args["limit"]; ok {
		if f, ok2 := v.(float64); ok2 && f > 0 {
			limit = int(f)
		}
	}

	fd, errCode := fileio.ReadFile(canonical, s.cfg)
	if errCode != "" {
		return mcp.NewToolResultError(errCode), nil
	}

	lines := fd.Lines
	totalLines := len(lines)

	start := offset - 1
	if start >= totalLines {
		start = totalLines
	}
	if start < 0 {
		start = 0
	}

	end := start + limit
	if end > totalLines {
		end = totalLines
	}
	var sb strings.Builder
	for i := start; i < end; i++ {
		sb.WriteString(hashline.FormatLine(lines[i], i+1))
		sb.WriteByte('\n')
	}

	if end < totalLines {
		fmt.Fprintf(&sb, "[Showing lines %d-%d of %d. Use offset=%d to read more.]\n",
			offset, offset+limit-1, totalLines, offset+limit)
	}

	s.recordSearchOp(canonical)
	return mcp.NewToolResultText(sb.String()), nil
}

func (s *Server) handleEdit(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	path, err := req.RequireString("path")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	canonical, errCode := fileio.CheckPath(path, s.cfg, true)
	if errCode != "" {
		return mcp.NewToolResultError(errCode), nil
	}

	args := req.GetArguments()
	editsRaw, ok := args["edits"]
	if !ok {
		return mcp.NewToolResultError("edits parameter required"), nil
	}

	editsJSON, err := json.Marshal(editsRaw)
	if err != nil {
		return mcp.NewToolResultError("cannot marshal edits: " + err.Error()), nil
	}

	var edits []editor.Edit
	if err := json.Unmarshal(editsJSON, &edits); err != nil {
		return mcp.NewToolResultError("cannot parse edits: " + err.Error()), nil
	}

	// Acquire lock before reading so hash verification sees a consistent snapshot.
	unlock, errCode := fileio.AcquireLock(canonical)
	if errCode != "" {
		return mcp.NewToolResultError(errCode), nil
	}
	defer unlock()

	fd, errCode := fileio.ReadFile(canonical, s.cfg)
	if errCode != "" {
		return mcp.NewToolResultError(errCode), nil
	}

	editReq := &editor.EditRequest{Path: path, Edits: edits}
	newLines, result, errCode, errDetail := editor.ApplyEdits(fd, editReq)

	if errCode == "SELF_CORRECT" {
		sc := editor.BuildSelfCorrectResult(fd.Lines, s.cfg.DefaultLimit, errDetail)
		jsonBytes, _ := json.Marshal(sc)
		return mcp.NewToolResultText(string(jsonBytes)), nil
	}
	if errCode == editor.ErrStaleRefs {
		var payload editor.StaleRefRepairResult
		if err := json.Unmarshal([]byte(errDetail), &payload); err == nil && payload.Status == "stale_refs" {
			return mcp.NewToolResultText(marshalGuardedStalePayload(payload, func(p *editor.StaleRefRepairResult) { s.applyStaleRetryGuard(canonical, p) })), nil
		}
		return mcp.NewToolResultText(errDetail), nil
	}

	if errCode != "" {
		msg := errCode
		if errDetail != "" {
			msg = errCode + ": " + errDetail
		}
		return mcp.NewToolResultError(msg), nil
	}

	if wc := fileio.WriteFile(fd, newLines); wc != "" {
		return mcp.NewToolResultError(wc), nil
	}
	searchWarn := s.consumeSearchWarning(canonical)
	multilineWarn := s.consumeMultilineWarning(canonical, editReq)
	resp := marshalSuccessResponse(fmt.Sprintf("OK: %d line(s) changed", result.LinesChanged), canonical, result, searchWarn, multilineWarn)
	return mcp.NewToolResultText(resp), nil
}

func (s *Server) handleWrite(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	path, err := req.RequireString("path")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	content, err := req.RequireString("content")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	// Quick prefix check before MkdirAll — prevents creating directories outside root.
	// CheckPath does the authoritative symlink-resolved check after parents exist.
	cleaned := filepath.Clean(path)
	if !strings.HasPrefix(cleaned, s.cfg.Root+string(os.PathSeparator)) {
		return mcp.NewToolResultError(fileio.ErrPathOutsideRoot), nil
	}

	// Create parent dirs first so CheckPath can EvalSymlinks on the parent.
	if err := os.MkdirAll(filepath.Dir(cleaned), 0o755); err != nil {
		return mcp.NewToolResultError("cannot create parent dirs: " + err.Error()), nil
	}

	canonical, errCode := fileio.CheckPath(path, s.cfg, false)
	if errCode != "" {
		return mcp.NewToolResultError(errCode), nil
	}
	// Reject if file already exists.
	if _, statErr := os.Stat(canonical); statErr == nil {
		return mcp.NewToolResultError(editor.ErrFileExists), nil
	}

	// Atomic write: temp file (0600) → chmod 0644 → hard-link (no-replace) → remove tmp. §9.1.
	// os.Link fails with EEXIST if the destination was created concurrently,
	// preventing the race between Stat and rename.
	tmpPath := fmt.Sprintf("%s.%d.%s.lapp.tmp", canonical, os.Getpid(), fileio.RandomHex(8))
	f, createErr := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if createErr != nil {
		if os.IsPermission(createErr) {
			return mcp.NewToolResultError(fileio.ErrPermissionDenied), nil
		}
		return mcp.NewToolResultError("cannot create temp file: " + createErr.Error()), nil
	}
	if _, writeErr := f.WriteString(content); writeErr != nil {
		f.Close()
		os.Remove(tmpPath)
		if os.IsPermission(writeErr) {
			return mcp.NewToolResultError(fileio.ErrPermissionDenied), nil
		}
		return mcp.NewToolResultError("cannot write file: " + writeErr.Error()), nil
	}
	if closeErr := f.Close(); closeErr != nil {
		os.Remove(tmpPath)
		return mcp.NewToolResultError("cannot write file: " + closeErr.Error()), nil
	}
	// Restore conventional source file permissions before making it visible.
	if chmodErr := os.Chmod(tmpPath, 0o644); chmodErr != nil {
		os.Remove(tmpPath)
		return mcp.NewToolResultError("cannot set file permissions: " + chmodErr.Error()), nil
	}
	// os.Link is atomic on POSIX: fails with EEXIST if destination exists.
	if linkErr := os.Link(tmpPath, canonical); linkErr != nil {
		os.Remove(tmpPath)
		if os.IsExist(linkErr) {
			return mcp.NewToolResultError(editor.ErrFileExists), nil
		}
		return mcp.NewToolResultError("cannot create file: " + linkErr.Error()), nil
	}
	// Link succeeded; remove the temp path (the inode now has two links).
	os.Remove(tmpPath)

	lines := strings.Count(content, "\n")
	if content != "" && content[len(content)-1] != '\n' {
		lines++
	}
	resp := marshalSuccessResponse(fmt.Sprintf("OK: created %s (%d lines)", path, lines), canonical, nil)
	return mcp.NewToolResultText(resp), nil
}

var literalRegexEscapeReplacer = strings.NewReplacer(
	`\[`, `[`,
	`\]`, `]`,
	`\(`, `(`,
	`\)`, `)`,
	`\.`, `.`,
	`\+`, `+`,
	`\*`, `*`,
	`\?`, `?`,
	`\{`, `{`,
	`\}`, `}`,
	`\|`, `|`,
	`\^`, `^`,
	`\$`, `$`,
	`\:`, `:`,
	`\-`, `-`,
	`\/`, `/`,
	`\,`, `,`,
	`\=`, `=`,
	`\"`, `"`,
	`\'`, `'`,
)

func literalVariants(pattern string) []string {
	variants := []string{pattern}
	deescaped := literalRegexEscapeReplacer.Replace(pattern)
	if deescaped != pattern {
		variants = append(variants, deescaped)
	}
	return variants
}

func (s *Server) handleGrep(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	pattern, err := req.RequireString("pattern")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	args := req.GetArguments()

	// Validate and resolve searchRoot — must be within --root.
	searchRoot := s.cfg.Root
	if v, ok := args["path"]; ok {
		if p, ok2 := v.(string); ok2 && p != "" {
			resolved, resolveErr := filepath.EvalSymlinks(filepath.Clean(p))
			if resolveErr != nil {
				return mcp.NewToolResultError(fileio.ErrFileNotFound), nil
			}
			// Enforce root containment on the resolved path.
			root := s.cfg.Root + string(os.PathSeparator)
			if !strings.HasPrefix(resolved, root) && resolved != s.cfg.Root {
				return mcp.NewToolResultError(fileio.ErrPathOutsideRoot), nil
			}
			searchRoot = resolved
		}
	}

	ctxLines := 2
	if v, ok := args["context"]; ok {
		if f, ok2 := v.(float64); ok2 && f >= 0 {
			ctxLines = int(f)
		}
	}

	const (
		fmtText       = "text"
		fmtStructured = "structured"
	)
	const maxCtxLines = 10
	if ctxLines > maxCtxLines {
		ctxLines = maxCtxLines
	}

	format := fmtText
	if v, ok := args["format"]; ok {
		if s, ok2 := v.(string); ok2 && s != "" {
			format = s
		}
	}
	if format != fmtText && format != fmtStructured {
		return mcp.NewToolResultError("invalid format: must be text or structured"), nil
	}

	literal := false
	if v, ok := args["literal"]; ok {
		if b, ok2 := v.(bool); ok2 && b {
			literal = true
		}
	}

	var re *regexp.Regexp
	if !literal {
		re, err = regexp.Compile(pattern)
		if err != nil {
			return mcp.NewToolResultError("invalid pattern: " + err.Error()), nil
		}
	}

	const maxGrepFiles = 100
	const maxStructuredMatches = 500
	maxOutputLines := s.cfg.DefaultLimit
	if maxOutputLines <= 0 {
		maxOutputLines = 2000
	}

	var sb strings.Builder
	filesMatched := 0
	outputLines := 0
	var structuredMatches []grepStructuredMatch

	errCapReached := errors.New("grep cap reached")

	walkErr := filepath.WalkDir(searchRoot, func(filePath string, d os.DirEntry, e error) error {
		if e != nil {
			return nil // ignore walk-level errors; continue traversal
		}
		if d.IsDir() {
			return nil
		}
		if filesMatched >= maxGrepFiles || (format == fmtText && outputLines >= maxOutputLines) || (format == fmtStructured && len(structuredMatches) >= maxStructuredMatches) {
			return errCapReached
		}

		canonical, errCode := fileio.CheckPath(filePath, s.cfg, true)
		if errCode != "" {
			return nil
		}
		fd, errCode := fileio.ReadFile(canonical, s.cfg)
		if errCode != "" {
			return nil
		}

		lines := fd.Lines
		var matches []int
		if literal {
			variants := literalVariants(pattern)
			for i, line := range lines {
				for _, v := range variants {
					if strings.Contains(line, v) {
						matches = append(matches, i+1)
						break
					}
				}
			}
		} else {
			for i, line := range lines {
				if re.MatchString(line) {
					matches = append(matches, i+1)
				}
			}
		}
		if len(matches) == 0 {
			return nil
		}

		filesMatched++
		if format == fmtStructured {
			for _, m := range matches {
				before := []string{}
				after := []string{}
				for k := max(1, m-ctxLines); k < m; k++ {
					before = append(before, hashline.FormatLine(lines[k-1], k))
				}
				for k := m + 1; k <= min(len(lines), m+ctxLines); k++ {
					after = append(after, hashline.FormatLine(lines[k-1], k))
				}
				structuredMatches = append(structuredMatches, grepStructuredMatch{
					Path:          canonical,
					Anchor:        fmt.Sprintf("%d#%s", m, hashline.HashLine(lines[m-1], m)),
					LineNumber:    m,
					Line:          lines[m-1],
					ContextBefore: before,
					ContextAfter:  after,
				})
			}
			s.recordSearchOp(canonical)
			return nil
		}

		sb.WriteString(filePath + ":\n")
		outputLines++

		display := make(map[int]bool)
		for _, m := range matches {
			for k := m - ctxLines; k <= m+ctxLines; k++ {
				if k >= 1 && k <= len(lines) {
					display[k] = true
				}
			}
		}
		matchSet := make(map[int]bool)
		for _, m := range matches {
			matchSet[m] = true
		}

		prev := 0
		for k := 1; k <= len(lines); k++ {
			if !display[k] {
				continue
			}
			if outputLines >= maxOutputLines {
				sb.WriteString("    ... [output truncated]\n")
				return errCapReached
			}
			if prev > 0 && k > prev+1 {
				sb.WriteString("    ...\n")
				outputLines++
			}
			prefix := "    "
			if matchSet[k] {
				prefix = ">>> "
			}
			sb.WriteString(prefix + hashline.FormatLine(lines[k-1], k) + "\n")
			outputLines++
			prev = k
		}
		s.recordSearchOp(canonical)
		return nil
	})

	if walkErr != nil && walkErr != errCapReached {
		// Unreachable: walk callback never returns non-nil (walk errors
		// are swallowed, only errCapReached escapes). Kept as safety net.
		return mcp.NewToolResultError("grep error: " + walkErr.Error()), nil
	}
	if format == "structured" {
		jsonBytes, _ := json.Marshal(grepStructuredResult{Matches: structuredMatches, Truncated: walkErr == errCapReached})
		return mcp.NewToolResultText(string(jsonBytes)), nil
	}
	if walkErr == errCapReached {
		fmt.Fprintf(&sb, "\n[Results truncated: showed %d files / %d lines. Narrow your pattern or path.]", filesMatched, outputLines)
	}

	result := sb.String()
	if result == "" {
		result = "No matches found."
	}
	return mcp.NewToolResultText(result), nil
}

type blockMatch struct {
	Path    string   `json:"path"`
	Start   string   `json:"start"`
	End     string   `json:"end"`
	Preview []string `json:"preview"`
}

type findBlockResult struct {
	Matches []blockMatch `json:"matches"`
}

type patchHunk struct {
	oldBlock []string
	newBlock []string
}

func parseUnifiedPatch(content string) ([]patchHunk, error) {
	content = editor.NormalizeNewlines(content)
	var hunks []patchHunk
	var oldBlock, newBlock []string
	inHunk := false
	seenFile := false

	flush := func() {
		if !inHunk {
			return
		}
		hunks = append(hunks, patchHunk{oldBlock: append([]string(nil), oldBlock...), newBlock: append([]string(nil), newBlock...)})
		oldBlock, newBlock = nil, nil
		inHunk = false
	}

	for _, line := range strings.Split(content, "\n") {
		switch {
		case strings.HasPrefix(line, "--- "):
			if seenFile {
				return nil, fmt.Errorf("patch must target exactly one file")
			}
		case strings.HasPrefix(line, "+++ "):
			seenFile = true
		case strings.HasPrefix(line, "@@ "):
			flush()
			inHunk = true
		case line == `\ No newline at end of file`:
			continue
		case !inHunk:
			continue
		case strings.HasPrefix(line, " "):
			oldBlock = append(oldBlock, line[1:])
			newBlock = append(newBlock, line[1:])
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			oldBlock = append(oldBlock, line[1:])
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			newBlock = append(newBlock, line[1:])
		default:
			return nil, fmt.Errorf("unsupported patch line: %q", line)
		}
	}
	flush()
	if len(hunks) == 0 {
		return nil, fmt.Errorf("patch must contain at least one @@ hunk")
	}
	return hunks, nil
}

type grepStructuredMatch struct {
	Path          string   `json:"path"`
	Anchor        string   `json:"anchor"`
	LineNumber    int      `json:"line_number"`
	Line          string   `json:"line"`
	ContextBefore []string `json:"context_before"`
	ContextAfter  []string `json:"context_after"`
}

type grepStructuredResult struct {
	Matches   []grepStructuredMatch `json:"matches"`
	Truncated bool                  `json:"truncated,omitempty"`
}

func splitSearchContent(content string) []string {
	content = editor.NormalizeNewlines(content)
	parts := strings.Split(content, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func stripSharedIndent(lines []string) []string {
	minIndent := -1
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			continue
		}
		indent := len(line) - len(trimmed)
		if minIndent == -1 || indent < minIndent {
			minIndent = indent
		}
	}
	if minIndent <= 0 {
		return lines
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			out[i] = ""
			continue
		}
		if len(line) >= minIndent {
			out[i] = line[minIndent:]
		} else {
			out[i] = trimmed
		}
	}
	return out
}

func sharedIndentString(lines []string) string {
	minIndent := -1
	indentStr := ""
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			continue
		}
		indent := len(line) - len(trimmed)
		if minIndent == -1 || indent < minIndent {
			minIndent = indent
			indentStr = line[:indent]
		}
	}
	return indentStr
}

func rebaseBlockIndent(oldBlock, newBlock []string) []string {
	if len(newBlock) == 0 {
		return newBlock
	}
	oldIndent := sharedIndentString(oldBlock)
	newBase := stripSharedIndent(newBlock)
	// stripSharedIndent returns the normalized lines; rebuild using the old base indent.
	out := make([]string, len(newBase))
	for i, line := range newBase {
		if line == "" {
			out[i] = ""
			continue
		}
		out[i] = oldIndent + line
	}
	return out
}

func findBlockRanges(lines, needle []string, normalizeWhitespace bool) []blockRange {
	needleNorm := needle
	if normalizeWhitespace {
		needleNorm = stripSharedIndent(needle)
	}
	var matches []blockRange
	for i := 0; i+len(needle) <= len(lines); i++ {
		window := lines[i : i+len(needle)]
		windowCmp := window
		if normalizeWhitespace {
			windowCmp = stripSharedIndent(window)
		}
		ok := true
		for j := range needleNorm {
			if windowCmp[j] != needleNorm[j] {
				ok = false
				break
			}
		}
		if ok {
			matches = append(matches, blockRange{start: i + 1, end: i + len(needle)})
		}
	}
	return matches
}

func bestSimilarBlockRange(lines, needle []string, normalizeWhitespace bool) (blockRange, []int, bool) {
	if len(needle) == 0 || len(lines) < len(needle) {
		return blockRange{}, nil, false
	}
	needleNorm := needle
	if normalizeWhitespace {
		needleNorm = stripSharedIndent(needle)
	}
	bestScore := -1
	var best blockRange
	var bestMismatch []int
	for i := 0; i+len(needle) <= len(lines); i++ {
		window := lines[i : i+len(needle)]
		windowCmp := window
		if normalizeWhitespace {
			windowCmp = stripSharedIndent(window)
		}
		score := 0
		mismatch := []int{}
		for j := range needleNorm {
			if windowCmp[j] == needleNorm[j] {
				score++
			} else {
				mismatch = append(mismatch, i+j+1)
			}
		}
		if score > bestScore {
			bestScore = score
			best = blockRange{start: i + 1, end: i + len(needle)}
			bestMismatch = mismatch
		}
	}
	if bestScore <= 0 || len(bestMismatch) == 0 {
		return blockRange{}, nil, false
	}
	return best, bestMismatch, true
}

type blockRange struct {
	start int
	end   int
}

func buildBlockStaleRepairPayload(lines []string, best blockRange, mismatchLines []int, message string) editor.StaleRefRepairResult {
	changed := make([]editor.StaleRefRepairLine, 0, len(mismatchLines))
	for _, lineNum := range mismatchLines {
		if lineNum < 1 || lineNum > len(lines) {
			continue
		}
		line := lines[lineNum-1]
		changed = append(changed, editor.StaleRefRepairLine{
			Anchor:     fmt.Sprintf("%d#%s", lineNum, hashline.HashLine(line, lineNum)),
			LineNumber: lineNum,
			Line:       line,
		})
	}
	return editor.StaleRefRepairResult{
		Status:    "stale_refs",
		ErrorCode: editor.ErrHashMismatch,
		Message:   message,
		Count:     len(changed),
		Changed:   changed,
		Note:      fmt.Sprintf("Closest matching region is lines %d-%d. Use the returned anchors to re-read or rebuild the edit.", best.start, best.end),
	}
}

func (s *Server) handleFindBlock(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	path, err := req.RequireString("path")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	content, err := req.RequireString("content")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	args := req.GetArguments()
	literal := true
	if v, ok := args["literal"]; ok {
		if b, ok2 := v.(bool); ok2 {
			literal = b
		}
	}
	normalizeWhitespace := true
	if v, ok := args["normalize_whitespace"]; ok {
		if b, ok2 := v.(bool); ok2 {
			normalizeWhitespace = b
		}
	}
	if !literal {
		return mcp.NewToolResultError("regex block search not supported; use literal=true"), nil
	}

	canonical, errCode := fileio.CheckPath(path, s.cfg, true)
	if errCode != "" {
		return mcp.NewToolResultError(errCode), nil
	}
	fd, errCode := fileio.ReadFile(canonical, s.cfg)
	if errCode != "" {
		return mcp.NewToolResultError(errCode), nil
	}
	needle := splitSearchContent(content)
	if len(needle) == 0 {
		return mcp.NewToolResultError("content parameter required"), nil
	}
	lines := fd.Lines
	var matches []blockMatch
	for _, m := range findBlockRanges(lines, needle, normalizeWhitespace) {
		startLine := m.start
		endLine := m.end
		preview := make([]string, 0, len(needle))
		for k := startLine; k <= endLine; k++ {
			preview = append(preview, hashline.FormatLine(lines[k-1], k))
		}
		matches = append(matches, blockMatch{
			Path:    canonical,
			Start:   fmt.Sprintf("%d#%s", startLine, hashline.HashLine(lines[startLine-1], startLine)),
			End:     fmt.Sprintf("%d#%s", endLine, hashline.HashLine(lines[endLine-1], endLine)),
			Preview: preview,
		})
	}
	s.recordFindBlock(canonical)
	s.recordSearchOp(canonical)
	jsonBytes, _ := json.Marshal(findBlockResult{Matches: matches})
	return mcp.NewToolResultText(string(jsonBytes)), nil
}

func (s *Server) handleInsertBlock(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	path, err := req.RequireString("path")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	anchorContent, err := req.RequireString("anchor_content")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	newContent, err := req.RequireString("new_content")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	position, err := req.RequireString("position")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if position != "before" && position != "after" {
		return mcp.NewToolResultError("position must be 'before' or 'after'"), nil
	}
	args := req.GetArguments()
	normalizeWhitespace := true
	if v, ok := args["normalize_whitespace"]; ok {
		if b, ok2 := v.(bool); ok2 {
			normalizeWhitespace = b
		}
	}

	canonical, errCode := fileio.CheckPath(path, s.cfg, true)
	if errCode != "" {
		return mcp.NewToolResultError(errCode), nil
	}
	unlock, errCode := fileio.AcquireLock(canonical)
	if errCode != "" {
		return mcp.NewToolResultError(errCode), nil
	}
	defer unlock()
	fd, errCode := fileio.ReadFile(canonical, s.cfg)
	if errCode != "" {
		return mcp.NewToolResultError(errCode), nil
	}
	needle := splitSearchContent(anchorContent)
	if len(needle) == 0 {
		return mcp.NewToolResultError("anchor_content parameter required"), nil
	}
	ranges := findBlockRanges(fd.Lines, needle, normalizeWhitespace)
	if len(ranges) == 0 {
		if best, mismatchLines, ok := bestSimilarBlockRange(fd.Lines, needle, normalizeWhitespace); ok {
			payload := buildBlockStaleRepairPayload(fd.Lines, best, mismatchLines, "Anchor block changed since last read. Retry with the updated refs below.")
			return mcp.NewToolResultText(marshalGuardedStalePayload(payload, func(p *editor.StaleRefRepairResult) { s.applyStaleRetryGuard(canonical, p) })), nil
		}
		return mcp.NewToolResultError("anchor block not found"), nil
	}
	if len(ranges) > 1 {
		return mcp.NewToolResultError(fmt.Sprintf("anchor block matched %d ranges; narrow the block or use lapp_find_block first", len(ranges))), nil
	}
	r := ranges[0]
	anchorLine := r.start
	if position == "after" {
		anchorLine = r.end
	}
	anchorRef := fmt.Sprintf("%d#%s", anchorLine, hashline.HashLine(fd.Lines[anchorLine-1], anchorLine))
	adjustedNewContent := newContent
	if normalizeWhitespace {
		anchorBlock := fd.Lines[r.start-1 : r.end]
		newBlock := splitSearchContent(newContent)
		adjustedNewContent = strings.Join(rebaseBlockIndent(anchorBlock, newBlock), "\n")
	}
	editType := editor.EditInsertAfter
	if position == "before" {
		editType = editor.EditInsertBefore
	}
	edits := []editor.Edit{{Type: editType, Anchor: anchorRef, Content: &adjustedNewContent}}
	editReq := &editor.EditRequest{Path: path, Edits: edits}
	newLines, result, errCode, errDetail := editor.ApplyEdits(fd, editReq)
	if errCode == editor.ErrStaleRefs {
		var payload editor.StaleRefRepairResult
		if err := json.Unmarshal([]byte(errDetail), &payload); err == nil && payload.Status == "stale_refs" {
			return mcp.NewToolResultText(marshalGuardedStalePayload(payload, func(p *editor.StaleRefRepairResult) { s.applyStaleRetryGuard(canonical, p) })), nil
		}
		return mcp.NewToolResultText(errDetail), nil
	}
	if errCode != "" {
		msg := errCode
		if errDetail != "" {
			msg = errCode + ": " + errDetail
		}
		return mcp.NewToolResultError(msg), nil
	}
	s.resetMultilineTracking(canonical)
	if wc := fileio.WriteFile(fd, newLines); wc != "" {
		return mcp.NewToolResultError(wc), nil
	}
	searchWarn := s.consumeSearchWarning(canonical)
	resp := marshalSuccessResponse(fmt.Sprintf("OK: %d line(s) changed", result.LinesChanged), canonical, result, searchWarn)
	return mcp.NewToolResultText(resp), nil
}

func (s *Server) handleApplyPatch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	path, err := req.RequireString("path")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	patch, err := req.RequireString("patch")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	args := req.GetArguments()
	normalizeWhitespace := true
	if v, ok := args["normalize_whitespace"]; ok {
		if b, ok2 := v.(bool); ok2 {
			normalizeWhitespace = b
		}
	}

	hunks, parseErr := parseUnifiedPatch(patch)
	if parseErr != nil {
		return mcp.NewToolResultError(parseErr.Error()), nil
	}
	canonical, errCode := fileio.CheckPath(path, s.cfg, true)
	if errCode != "" {
		return mcp.NewToolResultError(errCode), nil
	}
	unlock, errCode := fileio.AcquireLock(canonical)
	if errCode != "" {
		return mcp.NewToolResultError(errCode), nil
	}
	defer unlock()
	fd, errCode := fileio.ReadFile(canonical, s.cfg)
	if errCode != "" {
		return mcp.NewToolResultError(errCode), nil
	}

	edits := make([]editor.Edit, 0, len(hunks))
	for _, hunk := range hunks {
		if len(hunk.oldBlock) == 0 {
			return mcp.NewToolResultError("pure insertion hunks without surrounding context are not supported; include context lines in the patch"), nil
		}
		ranges := findBlockRanges(fd.Lines, hunk.oldBlock, normalizeWhitespace)
		if len(ranges) == 0 {
			if best, mismatchLines, ok := bestSimilarBlockRange(fd.Lines, hunk.oldBlock, normalizeWhitespace); ok {
				payload := buildBlockStaleRepairPayload(fd.Lines, best, mismatchLines, "Patch target changed since last read. Retry with the updated refs below.")
				return mcp.NewToolResultText(marshalGuardedStalePayload(payload, func(p *editor.StaleRefRepairResult) { s.applyStaleRetryGuard(canonical, p) })), nil
			}
			return mcp.NewToolResultError("patch target block not found"), nil
		}
		if len(ranges) > 1 {
			return mcp.NewToolResultError(fmt.Sprintf("patch hunk matched %d ranges; narrow the context or apply smaller hunks", len(ranges))), nil
		}
		r := ranges[0]
		startRef := fmt.Sprintf("%d#%s", r.start, hashline.HashLine(fd.Lines[r.start-1], r.start))
		endRef := fmt.Sprintf("%d#%s", r.end, hashline.HashLine(fd.Lines[r.end-1], r.end))
		adjustedNewContent := strings.Join(hunk.newBlock, "\n")
		if normalizeWhitespace {
			oldBlock := fd.Lines[r.start-1 : r.end]
			adjustedNewContent = strings.Join(rebaseBlockIndent(oldBlock, hunk.newBlock), "\n")
		}
		edits = append(edits, editor.Edit{Type: editor.EditReplace, Start: startRef, End: endRef, Content: &adjustedNewContent})
	}

	editReq := &editor.EditRequest{Path: path, Edits: edits}
	newLines, result, errCode, errDetail := editor.ApplyEdits(fd, editReq)
	if errCode == editor.ErrStaleRefs {
		var payload editor.StaleRefRepairResult
		if err := json.Unmarshal([]byte(errDetail), &payload); err == nil && payload.Status == "stale_refs" {
			return mcp.NewToolResultText(marshalGuardedStalePayload(payload, func(p *editor.StaleRefRepairResult) { s.applyStaleRetryGuard(canonical, p) })), nil
		}
		return mcp.NewToolResultText(errDetail), nil
	}
	if errCode != "" {
		msg := errCode
		if errDetail != "" {
			msg = errCode + ": " + errDetail
		}
		return mcp.NewToolResultError(msg), nil
	}
	s.resetMultilineTracking(canonical)
	if wc := fileio.WriteFile(fd, newLines); wc != "" {
		return mcp.NewToolResultError(wc), nil
	}
	searchWarn := s.consumeSearchWarning(canonical)
	resp := marshalSuccessResponse(fmt.Sprintf("OK: %d line(s) changed", result.LinesChanged), canonical, result, searchWarn)
	return mcp.NewToolResultText(resp), nil
}

func (s *Server) handleReplaceBlock(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	path, err := req.RequireString("path")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	oldContent, err := req.RequireString("old_content")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	newContent, err := req.RequireString("new_content")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	args := req.GetArguments()
	normalizeWhitespace := true
	if v, ok := args["normalize_whitespace"]; ok {
		if b, ok2 := v.(bool); ok2 {
			normalizeWhitespace = b
		}
	}

	canonical, errCode := fileio.CheckPath(path, s.cfg, true)
	if errCode != "" {
		return mcp.NewToolResultError(errCode), nil
	}
	unlock, errCode := fileio.AcquireLock(canonical)
	if errCode != "" {
		return mcp.NewToolResultError(errCode), nil
	}
	defer unlock()
	fd, errCode := fileio.ReadFile(canonical, s.cfg)
	if errCode != "" {
		return mcp.NewToolResultError(errCode), nil
	}
	needle := splitSearchContent(oldContent)
	if len(needle) == 0 {
		return mcp.NewToolResultError("old_content parameter required"), nil
	}
	ranges := findBlockRanges(fd.Lines, needle, normalizeWhitespace)
	if len(ranges) == 0 {
		if best, mismatchLines, ok := bestSimilarBlockRange(fd.Lines, needle, normalizeWhitespace); ok {
			payload := buildBlockStaleRepairPayload(fd.Lines, best, mismatchLines, "Target block changed since last read. Retry with the updated refs below.")
			return mcp.NewToolResultText(marshalGuardedStalePayload(payload, func(p *editor.StaleRefRepairResult) { s.applyStaleRetryGuard(canonical, p) })), nil
		}
		return mcp.NewToolResultError("old block not found"), nil
	}
	if len(ranges) > 1 {
		return mcp.NewToolResultError(fmt.Sprintf("old block matched %d ranges; narrow the block or use lapp_find_block first", len(ranges))), nil
	}
	r := ranges[0]
	startRef := fmt.Sprintf("%d#%s", r.start, hashline.HashLine(fd.Lines[r.start-1], r.start))
	endRef := fmt.Sprintf("%d#%s", r.end, hashline.HashLine(fd.Lines[r.end-1], r.end))
	adjustedNewContent := newContent
	if normalizeWhitespace {
		oldBlock := fd.Lines[r.start-1 : r.end]
		newBlock := splitSearchContent(newContent)
		adjustedNewContent = strings.Join(rebaseBlockIndent(oldBlock, newBlock), "\n")
	}
	edits := []editor.Edit{{Type: editor.EditReplace, Start: startRef, End: endRef, Content: &adjustedNewContent}}
	editReq := &editor.EditRequest{Path: path, Edits: edits}
	newLines, result, errCode, errDetail := editor.ApplyEdits(fd, editReq)
	if errCode == editor.ErrStaleRefs {
		var payload editor.StaleRefRepairResult
		if err := json.Unmarshal([]byte(errDetail), &payload); err == nil && payload.Status == "stale_refs" {
			return mcp.NewToolResultText(marshalGuardedStalePayload(payload, func(p *editor.StaleRefRepairResult) { s.applyStaleRetryGuard(canonical, p) })), nil
		}
		return mcp.NewToolResultText(errDetail), nil
	}
	if errCode != "" {
		msg := errCode
		if errDetail != "" {
			msg = errCode + ": " + errDetail
		}
		return mcp.NewToolResultError(msg), nil
	}
	s.resetMultilineTracking(canonical)
	if wc := fileio.WriteFile(fd, newLines); wc != "" {
		return mcp.NewToolResultError(wc), nil
	}
	searchWarn := s.consumeSearchWarning(canonical)
	resp := marshalSuccessResponse(fmt.Sprintf("OK: %d line(s) changed", result.LinesChanged), canonical, result, searchWarn)
	return mcp.NewToolResultText(resp), nil
}
