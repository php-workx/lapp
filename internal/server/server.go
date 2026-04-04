package server

import (
	"context"
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
	fmt.Fprintln(os.Stderr, "lapp: add to CLAUDE.md → Prefer lapp_read/lapp_edit/lapp_write over built-in Read/Edit/Write")

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
		mcp.WithDescription(`Search files for a pattern and return matches with LINE#HASH references. Use the returned references directly in lapp_edit without a separate lapp_read call.`),
		mcp.WithString("pattern", mcp.Required(), mcp.Description("Regex pattern to search for")),
		mcp.WithString("path", mcp.Description("File or directory to search (defaults to root)")),
		mcp.WithNumber("context", mcp.Description("Lines of context around matches (default: 2)")),
	)
	s.mcpS.AddTool(grepTool, s.handleGrep)
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
		sc := editor.BuildSelfCorrectResult(fd.Lines, s.cfg.DefaultLimit)
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


	// Atomic write: write to temp then rename.
	tmpPath := fmt.Sprintf("%s.%d.lapp.tmp", canonical, os.Getpid())
	if err := os.WriteFile(tmpPath, []byte(content), 0644); err != nil {
		os.Remove(tmpPath)
		return mcp.NewToolResultError("cannot write file: " + err.Error()), nil
	}
	if err := os.Rename(tmpPath, canonical); err != nil {
		os.Remove(tmpPath)
		return mcp.NewToolResultError("cannot rename temp file: " + err.Error()), nil
	}

	lines := strings.Count(content, "\n")
	if len(content) > 0 && content[len(content)-1] != '\n' {
		lines++
	}
	return mcp.NewToolResultText(fmt.Sprintf("OK: created %s (%d lines)", path, lines)), nil
}

func (s *Server) handleGrep(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	pattern, err := req.RequireString("pattern")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	args := req.GetArguments()

	searchRoot := s.cfg.Root
	if v, ok := args["path"]; ok {
		if p, ok2 := v.(string); ok2 && p != "" {
			searchRoot = p
		}
	}

	ctxLines := 2
	if v, ok := args["context"]; ok {
		if f, ok2 := v.(float64); ok2 && f >= 0 {
			ctxLines = int(f)
		}
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return mcp.NewToolResultError("invalid pattern: " + err.Error()), nil
	}

	var sb strings.Builder
	walkErr := filepath.WalkDir(searchRoot, func(path string, d os.DirEntry, e error) error {
		if e != nil || d.IsDir() {
			return e
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		// Binary check: null byte in first 8192 bytes.
		probe := data
		if len(probe) > 8192 {
			probe = probe[:8192]
		}
		for _, b := range probe {
			if b == 0 {
				return nil
			}
		}

		fileLines := strings.Split(string(data), "\n")

		var matches []int
		for i, line := range fileLines {
			if re.MatchString(line) {
				matches = append(matches, i+1) // 1-indexed
			}
		}
		if len(matches) == 0 {
			return nil
		}

		sb.WriteString(path + ":\n")

		// Build display set with context around each match.
		display := make(map[int]bool)
		for _, m := range matches {
			for k := m - ctxLines; k <= m+ctxLines; k++ {
				if k >= 1 && k <= len(fileLines) {
					display[k] = true
				}
			}
		}

		matchSet := make(map[int]bool)
		for _, m := range matches {
			matchSet[m] = true
		}

		prev := 0
		for k := 1; k <= len(fileLines); k++ {
			if !display[k] {
				continue
			}
			if prev > 0 && k > prev+1 {
				sb.WriteString("    ...\n")
			}
			prefix := "    "
			if matchSet[k] {
				prefix = ">>> "
			}
			sb.WriteString(prefix + hashline.FormatLine(fileLines[k-1], k) + "\n")
			prev = k
		}
		return nil
	})

	if walkErr != nil {
		return mcp.NewToolResultError("grep error: " + walkErr.Error()), nil
	}

	result := sb.String()
	if result == "" {
		result = "No matches found."
	}
	return mcp.NewToolResultText(result), nil
}
