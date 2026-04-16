package editor

// Error codes returned as string constants so callers can compare with == without
// importing sentinel errors or dealing with error wrapping.
const (
	ErrFileNotFound     = "ERR_FILE_NOT_FOUND"
	ErrFileExists       = "ERR_FILE_EXISTS"
	ErrBinaryFile       = "ERR_BINARY_FILE"
	ErrInvalidEncoding  = "ERR_INVALID_ENCODING"
	ErrPermissionDenied = "ERR_PERMISSION_DENIED"
	ErrPathOutsideRoot  = "ERR_PATH_OUTSIDE_ROOT"
	ErrPathBlocked      = "ERR_PATH_BLOCKED"
	ErrInvalidEdit      = "ERR_INVALID_EDIT"
	ErrInvalidRange     = "ERR_INVALID_RANGE"
	ErrLineOutOfRange   = "ERR_LINE_OUT_OF_RANGE"
	ErrNoOp             = "ERR_NO_OP"
	ErrOverlappingEdits = "ERR_OVERLAPPING_EDITS"
	ErrTooManyEdits     = "ERR_TOO_MANY_EDITS"
	ErrLocked           = "ERR_LOCKED"
	ErrHashMismatch     = "ERR_HASH_MISMATCH"
	ErrStaleRefs        = "ERR_STALE_REFS"
)

// EditType identifies the kind of mutation an Edit performs.
type EditType string

const (
	EditReplace      EditType = "replace"
	EditInsertAfter  EditType = "insert_after"
	EditInsertBefore EditType = "insert_before"
	EditDelete       EditType = "delete"
)

// Edit describes a single mutation to a file.
//
// Anchor/Start/End are hashline refs (e.g. "5#KH"). Content is a pointer so
// callers can distinguish nil (field absent) from &"" (explicit empty string,
// which acts as delete-by-replacement).
type Edit struct {
	Type    EditType `json:"type"`
	Anchor  string   `json:"anchor,omitempty"`
	Start   string   `json:"start,omitempty"`
	End     string   `json:"end,omitempty"`
	Content *string  `json:"content,omitempty"` // nil=absent, &""=replace with empty (delete)
}

// EditRequest bundles a file path with the ordered list of edits to apply.
// At most 100 edits per request.
type EditRequest struct {
	Path  string `json:"path"`
	Edits []Edit `json:"edits"` // max 100
}

// EditResult reports the outcome of a successful edit operation.
type EditResult struct {
	Path         string `json:"path"`
	LinesChanged int    `json:"lines_changed"`
	Diff         string `json:"diff"` // unified diff
}

// SelfCorrectResult is returned when an edit cannot be applied because the
// agent has not (or no longer has) current file content. The response always
// carries the current hashline-formatted file so the agent can re-anchor.
type SelfCorrectResult struct {
	Status      string `json:"status"`           // always "needs_read_first"
	Message     string `json:"message"`
	FileContent string `json:"file_content"`     // hashline-formatted content
	// Note is always set in this implementation.
	// The spec §5.1 describes a two-phase flow (absent on first failure, set on second),
	// but MCP is stateless so consecutive-failure tracking is not possible.
	// This results in slightly more aggressive escalation: acceptable trade-off.
	// Spec deviation documented: pm-20260404-004.
	Note string `json:"note,omitempty"`
}

// StaleRefRepairResult is returned when refs are valid in shape but stale
// against the current file contents. It carries fresh local anchors so the
// model can retry without rereading the full file.
type StaleRefRepairResult struct {
	Status    string                `json:"status"`      // always "stale_refs"
	ErrorCode string                `json:"error_code"`  // usually ERR_HASH_MISMATCH
	Message   string                `json:"message"`
	Count     int                   `json:"count"`
	Changed   []StaleRefRepairLine  `json:"changed"`
	Note      string                `json:"note,omitempty"`
}

type StaleRefRepairLine struct {
	Anchor     string `json:"anchor"`
	LineNumber int    `json:"line_number"`
	Line       string `json:"line"`
}

