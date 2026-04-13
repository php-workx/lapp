package server

import (
	"context"
	"errors"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lapp-dev/lapp/internal/editor"
	"github.com/lapp-dev/lapp/internal/fileio"
	"github.com/lapp-dev/lapp/pkg/hashline"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

const version = "0.1.0"

// Server wraps an MCPServer with project configuration.
type Server struct {
	cfg  *fileio.Config
	mcpS *mcpserver.MCPServer
}

// New creates and configures an MCP server. Emits a CLAUDE.md hint to stderr.
func New(cfg *fileio.Config) *Server {
	fmt.Fprintln(os.Stderr, "lapp: add to CLAUDE.md → Prefer lapp_read/lapp_edit/lapp_write/lapp_grep over built-in Read/Edit/Write/Grep")

	s := &Server{cfg: cfg}
	s.mcpS = mcpserver.NewMCPServer("lapp", version, mcpserver.WithToolCapabilities(false))
	s.registerTools()
	return s
}

// Start begins serving MCP over stdio.
func (s *Server) Start() error {
	return mcpserver.ServeStdio(s.mcpS)
}

func (s *Server) registerTools() {
	// lapp_read
	readTool := mcp.NewTool("lapp_read",
		mcp.WithDescription(`Read a file with content-hash-tagged line references. Each line is prefixed with LINE#HASH: where LINE is the line number and HASH is a 2-character content fingerprint. Hash references from any page remain valid for the entire file — you may paginate with offset/limit and combine references across pages. Use lapp_edit to modify files read with this tool.`),
		mcp.WithString("path", mcp.Required(), mcp.Description("Absolute path to the file (must be within the configured root)")),
		mcp.WithNumber("offset", mcp.Description("Start line, 1-indexed (default: 1)")),
		mcp.WithNumber("limit", mcp.Description("Max lines to return (default: server default, typically 2000)")),
	)
	s.mcpS.AddTool(readTool, s.handleRead)

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
	s.mcpS.AddTool(editTool, s.handleEdit)

	// lapp_write
	writeTool := mcp.NewTool("lapp_write",
		mcp.WithDescription(`Create a new file with the given content. For new files only — returns an error if the file already exists. To modify an existing file, use lapp_read + lapp_edit instead, which is faster and safer. Parent directories are created automatically.`),
		mcp.WithString("path", mcp.Required(), mcp.Description("Absolute path to the new file")),
		mcp.WithString("content", mcp.Required(), mcp.Description("Complete file content")),
	)
	s.mcpS.AddTool(writeTool, s.handleWrite)

	// lapp_grep
	grepTool := mcp.NewTool("lapp_grep",
		mcp.WithDescription(`Search files for a pattern and return matches with LINE#HASH references. Use the returned references directly in lapp_edit without a separate lapp_read call. Use literal=true when searching for code that contains regex special characters such as ( ) \ . + * ? [ ] ^ $ |. Use format=structured for machine-readable anchors, line text, and context.`),
		mcp.WithString("pattern", mcp.Required(), mcp.Description("Pattern to search for. Interpreted as a regex unless literal=true")),
		mcp.WithString("path", mcp.Description("File or directory to search (defaults to root)")),
		mcp.WithNumber("context", mcp.Description("Lines of context around matches (default: 2)")),
		mcp.WithBoolean("literal", mcp.Description("If true, treat pattern as a fixed string (no regex interpretation). Use when the search term contains code or regex metacharacters")),
		mcp.WithString("format", mcp.Description("Output format: text (default) or structured")),
	)
	s.mcpS.AddTool(grepTool, s.handleGrep)

	// lapp_find_block
	findBlockTool := mcp.NewTool("lapp_find_block",
		mcp.WithDescription(`Find an exact multi-line code block in a file and return start/end LINE#HASH references usable directly in lapp_edit. Use this for multi-line replacements where grepping the first and last line separately is ambiguous. By default, matching ignores shared leading indentation differences across the whole block.`),
		mcp.WithString("path", mcp.Required(), mcp.Description("Absolute path to the file to search")),
		mcp.WithString("content", mcp.Required(), mcp.Description("Exact block content to find. Preserve line breaks exactly.")),
		mcp.WithBoolean("literal", mcp.Description("If true, treat content as a literal block (default true). Regex block search is not currently supported.")),
		mcp.WithBoolean("normalize_whitespace", mcp.Description("If false, require exact indentation match. Default true ignores shared leading indentation differences when matching the block.")),
	)
	s.mcpS.AddTool(findBlockTool, s.handleFindBlock)

	// lapp_replace_block
	replaceBlockTool := mcp.NewTool("lapp_replace_block",
		mcp.WithDescription(`Replace one exact multi-line block in a file. Provide old_content and new_content; lapp will find the old block, resolve start/end refs internally, and apply one range replacement. By default, matching ignores shared leading indentation differences across the whole block.`),
		mcp.WithString("path", mcp.Required(), mcp.Description("Absolute path to the file to modify")),
		mcp.WithString("old_content", mcp.Required(), mcp.Description("Exact old block to replace.")),
		mcp.WithString("new_content", mcp.Required(), mcp.Description("Replacement block content.")),
		mcp.WithBoolean("normalize_whitespace", mcp.Description("If false, require exact indentation match. Default true ignores shared leading indentation differences when matching the old block.")),
	)
	s.mcpS.AddTool(replaceBlockTool, s.handleReplaceBlock)
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
		sb.WriteString(fmt.Sprintf("[Showing lines %d-%d of %d. Use offset=%d to read more.]\n",
			offset, offset+limit-1, totalLines, offset+limit))
	}

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

	resp := fmt.Sprintf("OK: %d line(s) changed\n%s", result.LinesChanged, result.Diff)
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
	if err := os.MkdirAll(filepath.Dir(cleaned), 0755); err != nil {
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


	// Atomic write: temp file (0600) → chmod 0644 → rename. §9.1.
	// New files created by lapp_write default to 0644 (conventional source file permissions).
	tmpPath := fmt.Sprintf("%s.%d.%s.lapp.tmp", canonical, os.Getpid(), fileio.RandomHex(8))
	f, createErr := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if createErr != nil {
		if os.IsPermission(createErr) {
			return mcp.NewToolResultError(fileio.ErrPermissionDenied), nil
		}
		return mcp.NewToolResultError("cannot create temp file: " + createErr.Error()), nil
	}
	if _, writeErr := f.Write([]byte(content)); writeErr != nil {
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
	// Restore conventional source file permissions before rename makes it visible.
	if chmodErr := os.Chmod(tmpPath, 0644); chmodErr != nil {
		os.Remove(tmpPath)
		return mcp.NewToolResultError("cannot set file permissions: " + chmodErr.Error()), nil
	}
	if errCode := fileio.RenameAtomic(tmpPath, canonical); errCode != "" {
		os.Remove(tmpPath)
		return mcp.NewToolResultError(errCode), nil
	}

	lines := strings.Count(content, "\n")
	if len(content) > 0 && content[len(content)-1] != '\n' {
		lines++
	}
	return mcp.NewToolResultText(fmt.Sprintf("OK: created %s (%d lines)", path, lines)), nil
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

	const maxCtxLines = 10
	if ctxLines > maxCtxLines {
		ctxLines = maxCtxLines
	}

	format := "text"
	if v, ok := args["format"]; ok {
		if s, ok2 := v.(string); ok2 && s != "" {
			format = s
		}
	}
	if format != "text" && format != "structured" {
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
			return e
		}
		if d.IsDir() {
			return nil
		}
		if filesMatched >= maxGrepFiles || (format == "text" && outputLines >= maxOutputLines) {
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
		if format == "structured" {
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
					Path: canonical,
					Anchor: fmt.Sprintf("%d#%s", m, hashline.HashLine(lines[m-1], m)),
					LineNumber: m,
					Line: lines[m-1],
					ContextBefore: before,
					ContextAfter: after,
				})
			}
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
		return nil
	})

	if walkErr != nil && walkErr != errCapReached {
		return mcp.NewToolResultError("grep error: " + walkErr.Error()), nil
	}
	if format == "structured" {
		jsonBytes, _ := json.Marshal(grepStructuredResult{Matches: structuredMatches})
		return mcp.NewToolResultText(string(jsonBytes)), nil
	}
	if walkErr == errCapReached {
		sb.WriteString(fmt.Sprintf("\n[Results truncated: showed %d files / %d lines. Narrow your pattern or path.]", filesMatched, outputLines))
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

type grepStructuredMatch struct {
	Path          string   `json:"path"`
	Anchor        string   `json:"anchor"`
	LineNumber    int      `json:"line_number"`
	Line          string   `json:"line"`
	ContextBefore []string `json:"context_before"`
	ContextAfter  []string `json:"context_after"`
}

type grepStructuredResult struct {
	Matches []grepStructuredMatch `json:"matches"`
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

type blockRange struct {
	start int
	end   int
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
			Path: canonical,
			Start: fmt.Sprintf("%d#%s", startLine, hashline.HashLine(lines[startLine-1], startLine)),
			End: fmt.Sprintf("%d#%s", endLine, hashline.HashLine(lines[endLine-1], endLine)),
			Preview: preview,
		})
	}
	jsonBytes, _ := json.Marshal(findBlockResult{Matches: matches})
	return mcp.NewToolResultText(string(jsonBytes)), nil
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
	resp := fmt.Sprintf("OK: %d line(s) changed\n%s", result.LinesChanged, result.Diff)
	return mcp.NewToolResultText(resp), nil
}