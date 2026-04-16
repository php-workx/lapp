package editor

import (
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"

	"github.com/lapp-dev/lapp/internal/fileio"
	"github.com/lapp-dev/lapp/pkg/hashline"
)

// hashlinePrefixRE matches the LINE#HASH: prefix produced by lapp_read.
var hashlinePrefixRE = regexp.MustCompile(`^\d+#[ZPMQVRWSNKTXJBYH]{2}:`)

// RefMismatch records a hash mismatch for one edit reference.
type RefMismatch struct {
	EditIndex  int
	Line       int    // 1-indexed line number
	Expected   string // hash from the stale reference
	Actual     string // current hash of the line (empty when OutOfRange)
	OutOfRange bool   // true when line number exceeds file length
}

// parsedEdit is the resolved form of an Edit after validation.
type parsedEdit struct {
	idx       int
	edit      Edit
	startLine int // first affected line (1-indexed)
	endLine   int // last affected line (1-indexed; == startLine for single-line ops)
}

// typePrecedence maps EditType to a sort key; lower = applied first in bottom-up pass.
func typePrecedence(t EditType) int {
	switch t {
	case EditDelete:
		return 0
	case EditReplace:
		return 1
	case EditInsertBefore:
		return 2
	case EditInsertAfter:
		return 3
	default:
		return 4
	}
}

// SanitizeContent strips hashline prefixes and unified-diff markers if they
// appear uniformly on all non-empty lines (§9.5).
func SanitizeContent(content string) string {
	lines := strings.Split(content, "\n")

	// Pass 1: strip hashline prefixes if ALL non-empty lines carry them.
	allHashline := true
	for _, l := range lines {
		if l == "" {
			continue
		}
		if !hashlinePrefixRE.MatchString(l) {
			allHashline = false
			break
		}
	}
	if allHashline {
		for i, l := range lines {
			if l == "" {
				continue
			}
			// Strip up to and including the first ':'
			if idx := strings.Index(l, ":"); idx >= 0 {
				lines[i] = l[idx+1:]
			}
		}
	}

	// Pass 2: strip leading '+' if ALL non-empty lines have one.
	allPlus := true
	for _, l := range lines {
		if l == "" {
			continue
		}
		if !strings.HasPrefix(l, "+") {
			allPlus = false
			break
		}
	}
	if allPlus {
		for i, l := range lines {
			if l == "" {
				continue
			}
			lines[i] = l[1:]
		}
	}

	return strings.Join(lines, "\n")
}

// NormalizeNewlines replaces literal `\n` (two-character sequence backslash-n)
// with a real newline character (§6.3). Only normalizes when content has NO real
// newlines — if real newlines are already present, the \n sequences are intentional
// code content (regex patterns, format strings, escape sequences) and must be preserved.
// Logs when normalization is applied.
func NormalizeNewlines(content string) string {
	if strings.Contains(content, `\n`) && !strings.Contains(content, "\n") {
		log.Printf("lapp: normalizing literal \\n sequence in content")
		return strings.ReplaceAll(content, `\n`, "\n")
	}
	return content
}

// splitContent normalizes and splits content into lines, discarding a trailing
// empty element when content ends with '\n' (§6.3).
func splitContent(content string) []string {
	content = NormalizeNewlines(content)
	content = SanitizeContent(content)
	parts := strings.Split(content, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func leadingIndent(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return s[:i]
}

func preserveSingleLineIndent(originalLine string, replacement []string) []string {
	if len(replacement) != 1 {
		return replacement
	}
	oldIndent := leadingIndent(originalLine)
	if oldIndent == "" {
		return replacement
	}
	trimmed := strings.TrimLeft(replacement[0], " \t")
	if trimmed == "" {
		return replacement
	}
	if leadingIndent(replacement[0]) == oldIndent {
		return replacement
	}
	return []string{oldIndent + trimmed}
}

// normalizeRef accepts refs copied or retyped from lapp_read/lapp_grep output.
// Valid inputs include:
//
//	LINE#HASH
//	LINE#HASH:
//	LINE#HASH:full line text
//	LINE:HASH            (model separator mistake; normalized to LINE#HASH)
//	LINE:HASH:full line  (same, with pasted display content)
//
// Special anchors 0: and EOF: are preserved unchanged.
func normalizeRef(ref string) string {
	if ref == "0:" || ref == "EOF:" {
		return ref
	}
	// Common model mistake: `245:BS` instead of `245#BS`.
	if m := regexp.MustCompile(`^(\d+):([A-Z]{2})(:.*)?$`).FindStringSubmatch(ref); m != nil {
		return m[1] + "#" + m[2]
	}
	if !strings.Contains(ref, "#") {
		return ref
	}
	// If the model pasted a full display line, keep only the LINE#HASH prefix.
	if i := strings.Index(ref, ":"); i != -1 {
		return ref[:i]
	}
	return ref
}

// ValidateEdits checks field combinations, parses all refs, and verifies hashes.
// Returns (parsedEdits, errCode, errDetail). errCode=="" on success.
func ValidateEdits(edits []Edit, lines []string) ([]parsedEdit, string, string) {
	if len(edits) > 100 {
		return nil, ErrTooManyEdits, fmt.Sprintf("batch of %d edits exceeds 100-edit limit", len(edits))
	}

	parsed := make([]parsedEdit, 0, len(edits))
	var mismatches []RefMismatch

	for i, e := range edits {
		pe, errCode, errDetail := validateOne(i, e, lines)
		if errCode != "" {
			return nil, errCode, errDetail
		}
		// Check for hash mismatch (ERR_INVALID_RANGE deferred above too).
		// Hash mismatch refs are recorded for a batch error later.
		parsed = append(parsed, pe)
	}

	// Second pass: verify all hashes. We do this after structural validation
	// so structural errors (field combos) get priority over stale refs.
	for i, pe := range parsed {
		e := pe.edit
		mms := verifyHashes(i, e, pe, lines)
		mismatches = append(mismatches, mms...)
	}

	if len(mismatches) > 0 {
		// ERR_LINE_OUT_OF_RANGE takes priority over ERR_HASH_MISMATCH.
		// Collect ALL out-of-range lines so the model can fix them in one retry.
		var outOfRange []string
		for _, m := range mismatches {
			if m.OutOfRange {
				outOfRange = append(outOfRange, fmt.Sprintf("%d", m.Line))
			}
		}
		if len(outOfRange) > 0 {
			return nil, ErrLineOutOfRange, fmt.Sprintf(
				"line(s) %s out of range (file has %d lines); re-read the file to get current line numbers",
				strings.Join(outOfRange, ", "), len(lines))
		}
		repair := BuildStaleRefRepairResult(mismatches, lines)
		jsonBytes, _ := json.Marshal(repair)
		return nil, ErrStaleRefs, string(jsonBytes)
	}

	return parsed, "", ""
}

// validateOne validates a single edit's field combination and parses its refs.
// Returns the parsed edit and any structural error. Hash mismatches are NOT
// checked here — they are collected separately so all are reported at once.
func validateOne(idx int, e Edit, lines []string) (parsedEdit, string, string) {
	switch e.Type {
	case EditReplace:
		if e.Content == nil {
			return parsedEdit{}, ErrInvalidEdit, fmt.Sprintf("edit[%d]: replace requires content", idx)
		}
		return parseAddressing(idx, e, lines, false)

	case EditInsertAfter, EditInsertBefore:
		if e.Start != "" || e.End != "" {
			return parsedEdit{}, ErrInvalidEdit, fmt.Sprintf("edit[%d]: %s does not support range addressing (start/end)", idx, e.Type)
		}
		if e.Anchor == "" {
			return parsedEdit{}, ErrInvalidEdit, fmt.Sprintf("edit[%d]: %s requires anchor", idx, e.Type)
		}
		if e.Content == nil || *e.Content == "" {
			return parsedEdit{}, ErrInvalidEdit, fmt.Sprintf("edit[%d]: %s requires non-empty content", idx, e.Type)
		}
		e.Anchor = normalizeRef(e.Anchor)
		lineNum, _, err := hashline.ParseRef(e.Anchor)
		if err != nil {
			return parsedEdit{}, ErrInvalidEdit, fmt.Sprintf("edit[%d]: invalid anchor ref %q: %v", idx, e.Anchor, err)
		}
		// Reject BOF/EOF special anchors for insert_before — §6.1 permits them
		// only for insert_after. insert_before with these produces silently wrong
		// results (inserts before last line instead of at end, etc.).
		if e.Type == EditInsertBefore && (lineNum == 0 || lineNum == -1) {
			return parsedEdit{}, ErrInvalidEdit, fmt.Sprintf("edit[%d]: BOF/EOF anchors are only valid for insert_after, not insert_before", idx)
		}
		// Resolve EOF anchor for insert_after.
		if lineNum == -1 {
			lineNum = len(lines)
		}
		return parsedEdit{idx: idx, edit: e, startLine: lineNum, endLine: lineNum}, "", ""

	case EditDelete:
		if e.Content != nil {
			return parsedEdit{}, ErrInvalidEdit, fmt.Sprintf("edit[%d]: delete must not include content", idx)
		}
		return parseAddressing(idx, e, lines, false)

	default:
		return parsedEdit{}, ErrInvalidEdit, fmt.Sprintf("edit[%d]: unknown type %q", idx, e.Type)
	}
}

// parseAddressing resolves the anchor or start/end pair for replace and delete.
func parseAddressing(idx int, e Edit, lines []string, _ bool) (parsedEdit, string, string) {
	if e.Anchor != "" {
		// Single-line mode.
		if e.Start != "" || e.End != "" {
			return parsedEdit{}, ErrInvalidEdit, fmt.Sprintf("edit[%d]: cannot use anchor together with start/end", idx)
		}
		e.Anchor = normalizeRef(e.Anchor)
		lineNum, _, err := hashline.ParseRef(e.Anchor)
		if err != nil {
			return parsedEdit{}, ErrInvalidEdit, fmt.Sprintf("edit[%d]: invalid anchor ref %q: %v", idx, e.Anchor, err)
		}
		if lineNum == 0 || lineNum == -1 {
			// BOF/EOF anchors only valid for insert_after; should not reach here.
			return parsedEdit{}, ErrInvalidEdit, fmt.Sprintf("edit[%d]: BOF/EOF anchors only valid for insert_after", idx)
		}
		return parsedEdit{idx: idx, edit: e, startLine: lineNum, endLine: lineNum}, "", ""
	}

	// Range mode.
	if e.Start == "" || e.End == "" {
		return parsedEdit{}, ErrInvalidEdit, fmt.Sprintf("edit[%d]: requires anchor or both start+end", idx)
	}
	e.Start = normalizeRef(e.Start)
	e.End = normalizeRef(e.End)
	startLine, _, err := hashline.ParseRef(e.Start)
	if err != nil {
		return parsedEdit{}, ErrInvalidEdit, fmt.Sprintf("edit[%d]: invalid start ref %q: %v", idx, e.Start, err)
	}
	endLine, _, err := hashline.ParseRef(e.End)
	if err != nil {
		return parsedEdit{}, ErrInvalidEdit, fmt.Sprintf("edit[%d]: invalid end ref %q: %v", idx, e.End, err)
	}
	if startLine == 0 || startLine == -1 || endLine == 0 || endLine == -1 {
		return parsedEdit{}, ErrInvalidEdit, fmt.Sprintf("edit[%d]: BOF/EOF anchors not allowed in range ops", idx)
	}
	if startLine > endLine {
		return parsedEdit{}, ErrInvalidRange, fmt.Sprintf("edit[%d]: start line %d > end line %d", idx, startLine, endLine)
	}
	return parsedEdit{idx: idx, edit: e, startLine: startLine, endLine: endLine}, "", ""
}

// verifyHashes checks hash references in a parsed edit against the current lines.
func verifyHashes(idx int, e Edit, pe parsedEdit, lines []string) []RefMismatch {
	var out []RefMismatch

	checkRef := func(ref string) {
		lineNum, expectedHash, err := hashline.ParseRef(ref)
		if err != nil || lineNum <= 0 {
			return // special anchors or bad refs caught earlier
		}
		if lineNum < 1 || lineNum > len(lines) {
			// Line number is outside the file — distinct from hash mismatch.
			// The model must re-read the file to learn the actual length.
			out = append(out, RefMismatch{EditIndex: idx, Line: lineNum, Expected: expectedHash, OutOfRange: true})
			return
		}
		actual := hashline.HashLine(lines[lineNum-1], lineNum)
		if actual != expectedHash {
			out = append(out, RefMismatch{EditIndex: idx, Line: lineNum, Expected: expectedHash, Actual: actual})
		}
	}

	switch {
	case e.Anchor != "":
		checkRef(e.Anchor)
	default:
		checkRef(e.Start)
		if e.End != e.Start {
			checkRef(e.End)
		}
	}
	return out
}

// DetectOverlaps returns pairs of edit indices that have overlapping line ranges.
func DetectOverlaps(parsed []parsedEdit) [][2]int {
	var conflicts [][2]int
	for i := 0; i < len(parsed); i++ {
		for j := i + 1; j < len(parsed); j++ {
			a, b := parsed[i], parsed[j]
			if overlaps(a, b) {
				conflicts = append(conflicts, [2]int{a.idx, b.idx})
			}
		}
	}
	return conflicts
}

// overlaps returns true when two parsed edits target intersecting line ranges.
// Two inserts on the same anchor are explicitly NOT overlapping (sequential).
func overlaps(a, b parsedEdit) bool {
	// Two inserts on the exact same anchor are sequential, not overlapping.
	sameAnchorInsert := func(x, y parsedEdit) bool {
		tx, ty := x.edit.Type, y.edit.Type
		if (tx == EditInsertAfter || tx == EditInsertBefore) &&
			(ty == EditInsertAfter || ty == EditInsertBefore) {
			return x.edit.Anchor == y.edit.Anchor && x.edit.Anchor != ""
		}
		return false
	}
	if sameAnchorInsert(a, b) {
		return false
	}
	// Ranges [a.start, a.end] and [b.start, b.end] overlap when they share at
	// least one line (closed intervals).
	return a.startLine <= b.endLine && b.startLine <= a.endLine
}

// FormatMismatchError builds the §8.1 error message with context lines and
// remapping table.
func FormatMismatchError(mismatches []RefMismatch, lines []string) string {
	const ctx = 2

	mismatchSet := make(map[int]RefMismatch)
	for _, m := range mismatches {
		mismatchSet[m.Line] = m
	}

	// Collect display lines (mismatch ± ctx context).
	displaySet := make(map[int]bool)
	for _, m := range mismatches {
		lo := m.Line - ctx
		if lo < 1 {
			lo = 1
		}
		hi := m.Line + ctx
		if hi > len(lines) {
			hi = len(lines)
		}
		for l := lo; l <= hi; l++ {
			displaySet[l] = true
		}
	}
	sorted := make([]int, 0, len(displaySet))
	for l := range displaySet {
		sorted = append(sorted, l)
	}
	sort.Ints(sorted)

	n := len(mismatches)
	suffix := "s have"
	if n == 1 {
		suffix = " has"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d line%s changed since last read. Use the updated LINE#HASH references below (>>> marks changed lines).", n, suffix)
	sb.WriteString("\n")

	prev := -1
	for _, lineNum := range sorted {
		if prev != -1 && lineNum > prev+1 {
			sb.WriteString("    ...\n")
		}
		prev = lineNum

		var content string
		if lineNum >= 1 && lineNum <= len(lines) {
			content = lines[lineNum-1]
		}
		currentHash := hashline.HashLine(content, lineNum)
		ref := fmt.Sprintf("%d#%s", lineNum, currentHash)
		if _, bad := mismatchSet[lineNum]; bad {
			fmt.Fprintf(&sb, ">>> %s:%s\n", ref, content)
		} else {
			fmt.Fprintf(&sb, "    %s:%s\n", ref, content)
		}
	}

	// Remapping table.
	sb.WriteString("\nStale → Current:\n")
	for _, m := range mismatches {
		if m.OutOfRange {
			// No current hash exists for a line beyond EOF — print actionable guidance.
			fmt.Fprintf(&sb, "  %d#%s \xe2\x86\x92 (line %d does not exist \xe2\x80\x94 re-read the file)\n",
				m.Line, m.Expected, m.Line)
			continue
		}
		var content string
		if m.Line >= 1 && m.Line <= len(lines) {
			content = lines[m.Line-1]
		}
		currentHash := hashline.HashLine(content, m.Line)
		fmt.Fprintf(&sb, "  %d#%s → %d#%s\n", m.Line, m.Expected, m.Line, currentHash)
	}

	return sb.String()
}

// BuildStaleRefRepairResult produces a human-readable repair payload for stale references.
func BuildStaleRefRepairResult(mismatches []RefMismatch, lines []string) *StaleRefRepairResult {
	seen := map[int]bool{}
	changed := []StaleRefRepairLine{}
	for _, m := range mismatches {
		if m.OutOfRange || seen[m.Line] || m.Line < 1 || m.Line > len(lines) {
			continue
		}
		seen[m.Line] = true
		line := lines[m.Line-1]
		anchor := fmt.Sprintf("%d#%s", m.Line, hashline.HashLine(line, m.Line))
		changed = append(changed, StaleRefRepairLine{
			Anchor:     anchor,
			LineNumber: m.Line,
			Line:       line,
		})
	}
	return &StaleRefRepairResult{
		Status:    "stale_refs",
		ErrorCode: ErrHashMismatch,
		Message:   fmt.Sprintf("%d line(s) changed since last read. Retry with the updated refs below.", len(changed)),
		Count:     len(changed),
		Changed:   changed,
		Note:      "Use the returned anchors directly in your retry instead of rereading the whole file.",
	}
}

// BuildSelfCorrectResult constructs the SelfCorrectResult returned when
// lapp_edit is called without usable refs. The message may explain the likely
// formatting mistake so the model can recover without guessing.
func BuildSelfCorrectResult(lines []string, limit int, message string) *SelfCorrectResult {
	displayLines := len(lines)
	if limit > 0 && limit < displayLines {
		displayLines = limit
	}
	formatted := make([]string, displayLines)
	for i := 0; i < displayLines; i++ {
		formatted[i] = hashline.FormatLine(lines[i], i+1)
	}
	var truncNote string
	if displayLines < len(lines) {
		truncNote = fmt.Sprintf("\n[Showing lines 1-%d of %d. Use offset parameter to read more.]", displayLines, len(lines))
	}
	if message == "" {
		message = "No valid LINE#HASH references found. Use the file_content below to construct your edits."
	}
	return &SelfCorrectResult{
		Status:      "needs_read_first",
		Message:     message,
		FileContent: strings.Join(formatted, "\n") + truncNote,
		Note:        "If unable to proceed after using these references, report the edit as blocked rather than retrying further.",
	}
}

func inferSelfCorrectMessage(edits []Edit) string {
	for _, e := range edits {
		for _, r := range []string{e.Anchor, e.Start, e.End} {
			if r == "" {
				continue
			}
			if regexp.MustCompile(`^\d+$`).MatchString(r) {
				return fmt.Sprintf("Ref %q is missing the #HASH part. Use the full LINE#HASH reference returned by lapp_read or lapp_grep.", r)
			}
			if strings.Contains(r, "#") && strings.Contains(r, ":") && !strings.HasSuffix(r, ":") {
				return fmt.Sprintf("Ref %q looks like a full display line. Use just the LINE#HASH prefix or paste the full line — lapp will extract it automatically.", r)
			}
		}
	}
	return ""
}

// IsNoOp returns true if the original and result line slices are identical.
func IsNoOp(original, result []string) bool {
	if len(original) != len(result) {
		return false
	}
	for i := range original {
		if original[i] != result[i] {
			return false
		}
	}
	return true
}

// ApplyEdits validates and applies a batch of edits to fd.Lines.
// Returns (newLines, result, errCode, errDetail). errCode=="" means success.
func ApplyEdits(fd *fileio.FileData, req *EditRequest) ([]string, *EditResult, string, string) {
	lines := fd.Lines

	// Self-correct detection: if the model supplied refs but NONE of them parse as
	// valid lapp addressing (N#XX, 0:, EOF:), the model likely used the built-in
	// Read tool instead of lapp_read and is sending plain-text addresses.
	// Return SELF_CORRECT so the server can return structured file content.
	//
	// Condition: at least one non-empty ref field exists AND no ref parses OK.
	// If ALL ref fields are empty, ValidateEdits catches it as ERR_INVALID_EDIT.
	hasAnyRef := false
	hasValidHashlineRef := false
	for _, e := range req.Edits {
		for _, r := range []string{e.Anchor, e.Start, e.End} {
			if r == "" {
				continue
			}
			hasAnyRef = true
			r = normalizeRef(r)
			if _, _, err := hashline.ParseRef(r); err == nil {
				hasValidHashlineRef = true
			}
		}
	}
	if hasAnyRef && !hasValidHashlineRef {
		return nil, nil, "SELF_CORRECT", inferSelfCorrectMessage(req.Edits)
	}

	parsed, errCode, errDetail := ValidateEdits(req.Edits, lines)
	if errCode != "" {
		return nil, nil, errCode, errDetail
	}

	conflicts := DetectOverlaps(parsed)
	if len(conflicts) > 0 {
		return nil, nil, ErrOverlappingEdits, fmt.Sprintf("overlapping edits: pairs %v", conflicts)
	}

	// Sort bottom-up (descending endLine, then type precedence, then idx).
	sort.SliceStable(parsed, func(i, j int) bool {
		a, b := parsed[i], parsed[j]
		if a.endLine != b.endLine {
			return a.endLine > b.endLine
		}
		pa, pb := typePrecedence(a.edit.Type), typePrecedence(b.edit.Type)
		if pa != pb {
			return pa < pb
		}
		// Same-anchor inserts: sort DESCENDING by idx so that the last insert
		// is applied first. Each subsequent (earlier-idx) insert splices at the
		// same position, pushing later inserts down — leaving them in original
		// array order in the final result.
		if (a.edit.Type == EditInsertAfter || a.edit.Type == EditInsertBefore) &&
			a.edit.Anchor == b.edit.Anchor && a.edit.Anchor != "" {
			return a.idx > b.idx
		}
		return a.idx < b.idx
	})

	// Apply edits bottom-up.
	result := make([]string, len(lines))
	copy(result, lines)

	for _, pe := range parsed {
		result = applyOne(pe, result)
	}

	if IsNoOp(lines, result) {
		return nil, nil, ErrNoOp, "all edits produce identical content"
	}

	diff, linesChanged := generateDiff(lines, result, req.Path)
	editResult := &EditResult{
		Path:         req.Path,
		LinesChanged: linesChanged,
		Diff:         diff,
	}
	return result, editResult, "", ""
}

// applyOne applies a single parsed edit to the line slice and returns the updated slice.
func applyOne(pe parsedEdit, lines []string) []string {
	e := pe.edit
	switch e.Type {
	case EditReplace:
		var newContent []string
		if e.Content != nil && *e.Content != "" {
			newContent = splitContent(*e.Content)
			if pe.startLine == pe.endLine && pe.startLine >= 1 && pe.startLine <= len(lines) {
				// Single-line anchored replacements should preserve the original line's
				// indentation unless the caller uses a broader range edit. Models often
				// change leading whitespace accidentally when rewriting one code line.
				newContent = preserveSingleLineIndent(lines[pe.startLine-1], newContent)
			}
		}
		// splice: replace [startLine-1 : endLine] with newContent
		start := pe.startLine - 1
		end := pe.endLine
		if start < 0 {
			start = 0
		}
		if end > len(lines) {
			end = len(lines)
		}
		return splice(lines, start, end, newContent)

	case EditInsertAfter:
		newContent := splitContent(*e.Content)
		pos := pe.startLine // insert AFTER line pe.startLine → splice at position pe.startLine
		// BOF anchor (lineNum==0) → insert at position 0
		if pe.startLine == 0 {
			pos = 0
		}
		return splice(lines, pos, pos, newContent)

	case EditInsertBefore:
		newContent := splitContent(*e.Content)
		pos := pe.startLine - 1 // insert BEFORE line → splice at position pe.startLine-1
		if pos < 0 {
			pos = 0
		}
		return splice(lines, pos, pos, newContent)

	case EditDelete:
		start := pe.startLine - 1
		end := pe.endLine
		if start < 0 {
			start = 0
		}
		if end > len(lines) {
			end = len(lines)
		}
		return splice(lines, start, end, nil)
	}
	return lines
}

// splice replaces lines[start:end] with replacement, returning the new slice.
func splice(lines []string, start, end int, replacement []string) []string {
	result := make([]string, 0, len(lines)-(end-start)+len(replacement))
	result = append(result, lines[:start]...)
	result = append(result, replacement...)
	result = append(result, lines[end:]...)
	return result
}

// generateDiff produces a simplified unified diff and counts changed lines.
func generateDiff(original, updated []string, path string) (string, int) {
	const ctxLines = 2

	// Guard: LCS has O(n×m) memory cost. For large files, fall back to a simple
	// positional diff that avoids allocation.
	const maxLCSLines = 5000
	if len(original) > maxLCSLines || len(updated) > maxLCSLines {
		// Count positions that differ (positional heuristic, not true LCS).
		shorter := min(len(original), len(updated))
		changed := 0
		for i := 0; i < shorter; i++ {
			if original[i] != updated[i] {
				changed++
			}
		}
		changed += abs(len(original) - len(updated))
		return fmt.Sprintf("--- a/%s\n+++ b/%s\n[diff omitted: file exceeds %d-line LCS limit]\n", path, path, maxLCSLines), changed
	}

	ops := lcs(original, updated)

	// Group ops into index-pair hunks [start, end).
	type hunk struct{ start, end int }
	var hunks []hunk
	i := 0
	for i < len(ops) {
		if ops[i].kind == ' ' {
			i++
			continue
		}
		// Found a change. Start hunk with ctxLines prefix.
		start := i - ctxLines
		if start < 0 {
			start = 0
		}
		// Extend through all close changes.
		end := i
		for end < len(ops) {
			if ops[end].kind != ' ' {
				end++
				continue
			}
			// Count the gap of context lines.
			gap, k := 0, end
			for k < len(ops) && ops[k].kind == ' ' {
				gap++
				k++
			}
			if gap > 2*ctxLines || k == len(ops) {
				break
			}
			end = k
		}
		// Add ctxLines suffix.
		end += ctxLines
		if end > len(ops) {
			end = len(ops)
		}
		hunks = append(hunks, hunk{start, end})
		i = end
	}

	if len(hunks) == 0 {
		return "", 0
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "--- a/%s\n", path)
	fmt.Fprintf(&sb, "+++ b/%s\n", path)

	added, removed := 0, 0
	// Track line numbers by walking ops from the start each hunk.
	oldBase, newBase := 1, 1 // line numbers at ops[0]
	opBase := 0

	for _, h := range hunks {
		// Advance base counters to h.start.
		for opBase < h.start {
			if ops[opBase].kind != '+' {
				oldBase++
			}
			if ops[opBase].kind != '-' {
				newBase++
			}
			opBase++
		}
		oldStart, newStart := oldBase, newBase
		oldCount, newCount := 0, 0
		for _, op := range ops[h.start:h.end] {
			switch op.kind {
			case ' ':
				oldCount++
				newCount++
			case '-':
				oldCount++
			case '+':
				newCount++
			}
		}
		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
		for _, op := range ops[h.start:h.end] {
			sb.WriteByte(byte(op.kind))
			sb.WriteString(op.line)
			sb.WriteByte('\n')
			switch op.kind {
			case '-':
				removed++
				oldBase++
			case '+':
				added++
				newBase++
			default:
				oldBase++
				newBase++
			}
			opBase++
		}
	}
	return sb.String(), added + removed
}

// editOp is a single unit in an LCS-based edit script.
type editOp struct {
	kind rune // ' ' = context, '-' = removed, '+' = added
	line string
}

// lcs computes the edit script between two line slices via O(n*m) LCS DP.
func lcs(a, b []string) []editOp {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] > dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}
	ops := make([]editOp, 0, n+m)
	i, j := n, m
	for i > 0 || j > 0 {
		if i > 0 && j > 0 && a[i-1] == b[j-1] {
			ops = append(ops, editOp{' ', a[i-1]})
			i--
			j--
		} else if j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]) {
			ops = append(ops, editOp{'+', b[j-1]})
			j--
		} else {
			ops = append(ops, editOp{'-', a[i-1]})
			i--
		}
	}
	// Reverse to get forward order.
	for l, r := 0, len(ops)-1; l < r; l, r = l+1, r-1 {
		ops[l], ops[r] = ops[r], ops[l]
	}
	return ops
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
