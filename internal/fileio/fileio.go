package fileio

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"time"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"
)

// Error codes returned by all fileio operations.
const (
	ErrFileNotFound    = "ERR_FILE_NOT_FOUND"
	ErrPathOutsideRoot = "ERR_PATH_OUTSIDE_ROOT"
	ErrPathBlocked     = "ERR_PATH_BLOCKED"
	ErrBinaryFile      = "ERR_BINARY_FILE"
	ErrInvalidEncoding = "ERR_INVALID_ENCODING"
	ErrLocked            = "ERR_LOCKED"
	ErrPermissionDenied = "ERR_PERMISSION_DENIED"
	ErrWriteFailed       = "ERR_WRITE_FAILED"
)

// Config holds server-wide settings passed from CLI flags.
type Config struct {
	Root          string   // canonical root path (absolute, resolved)
	BlockPatterns []string // glob patterns for blocked paths
	AllowPatterns []string // override patterns (remove from block list)
	DefaultLimit  int      // default lapp_read line limit
}

// FileData holds a parsed file's content and metadata.
type FileData struct {
	Lines          []string    // raw line content (no terminators)
	Terminators    []string    // per-line: "\r\n", "\n", or "" (last line)
	MajorityEnding string      // "\r\n" or "\n"
	HasBOM         bool
	Mode           fs.FileMode
	CanonicalPath  string
}

// DefaultBlockPatterns is the default set of sensitive-path globs (§9.8).
var DefaultBlockPatterns = []string{
	"**/.env",
	"**/.env.*",
	"**/secrets.*",
	"**/credentials.*",
	"**/*.pem",
	"**/*.key",
	"**/*.p12",
	"**/*.pfx",
	"**/.aws/credentials",
	"**/.aws/config",
}

// DefaultAllowPatterns are paths excluded from blocking even if matched by
// DefaultBlockPatterns (e.g. checked-in example env files are safe).
var DefaultAllowPatterns = []string{
	"**/.env.example",
	"**/.env.sample",
}

// ReadFile validates path, reads the file, and returns parsed content.
// On success errCode is "". On failure, the returned *FileData is nil.
func ReadFile(path string, cfg *Config) (*FileData, string) {
	canonical, code := CheckPath(path, cfg, true)
	if code != "" {
		return nil, code
	}

	raw, err := os.ReadFile(canonical)
	if err != nil {
		if os.IsPermission(err) {
			return nil, ErrPermissionDenied
		}
		return nil, ErrFileNotFound
	}

	// Binary check: null byte anywhere in the first 8192 bytes.
	probe := raw
	if len(probe) > 8192 {
		probe = probe[:8192]
	}
	if bytes.IndexByte(probe, 0) >= 0 {
		return nil, ErrBinaryFile
	}

	// UTF-8 validity (before BOM strip — BOM bytes are valid UTF-8).
	if !utf8.Valid(raw) {
		return nil, ErrInvalidEncoding
	}

	// BOM detection and stripping.
	hasBOM := false
	bom := []byte{0xEF, 0xBB, 0xBF}
	if bytes.HasPrefix(raw, bom) {
		hasBOM = true
		raw = raw[len(bom):]
	}

	// Line ending statistics on the (possibly BOM-stripped) data.
	crlfCount := bytes.Count(raw, []byte("\r\n"))
	lfCount := bytes.Count(raw, []byte("\n")) - crlfCount
	majorityEnding := "\n"
	if crlfCount > lfCount {
		majorityEnding = "\r\n"
	}

	lines, terminators := splitLines(raw)

	info, err := os.Stat(canonical)
	if err != nil {
		return nil, ErrFileNotFound
	}

	return &FileData{
		Lines:          lines,
		Terminators:    terminators,
		MajorityEnding: majorityEnding,
		HasBOM:         hasBOM,
		Mode:           info.Mode(),
		CanonicalPath:  canonical,
	}, ""
}

// WriteFile writes newLines back to fd.CanonicalPath atomically via a temp
// file + rename. Terminators from the original file are preserved for lines
// that existed before; extra lines use fd.MajorityEnding. The last line
// written never receives a trailing terminator unless the original had one
// and it is being preserved as a non-last line.
func WriteFile(fd *FileData, newLines []string) string {
	info, err := os.Stat(fd.CanonicalPath)
	if err != nil {
		return ErrWriteFailed
	}
	mode := info.Mode()

	tempPath := fmt.Sprintf("%s.%d.%s.lapp.tmp",
		fd.CanonicalPath, os.Getpid(), RandomHex(8))

	f, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrWriteFailed
	}

	cleanup := func() {
		f.Close()
		os.Remove(tempPath) // best-effort; ignore error
	}

	// Prepend UTF-8 BOM if the original had one.
	if fd.HasBOM {
		if _, err := f.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
			cleanup()
			return ErrWriteFailed
		}
	}

	for i, line := range newLines {
		isLast := i == len(newLines)-1

		var term string
		switch {
		case i < len(fd.Terminators):
			term = fd.Terminators[i]
			// Original last-line terminator was ""; if more lines follow we
			// must not merge them — use MajorityEnding to insert a separator.
			if !isLast && term == "" {
				term = fd.MajorityEnding
			}
		case !isLast:
			term = fd.MajorityEnding
		default:
			term = "" // extra last line: no trailing newline
		}

		if _, err := fmt.Fprintf(f, "%s%s", line, term); err != nil {
			cleanup()
			return ErrWriteFailed
		}
	}

	if err := f.Close(); err != nil {
		os.Remove(tempPath)
		return ErrWriteFailed
	}

	// Restore original permissions before the file becomes visible.
	if err := os.Chmod(tempPath, mode); err != nil {
		os.Remove(tempPath)
		return ErrWriteFailed
	}

	if errCode := renameAtomic(tempPath, fd.CanonicalPath); errCode != "" {
		os.Remove(tempPath)
		return errCode
	}

	return ""
}

// CheckPath validates that path is inside cfg.Root and not blocked by the
// block list. If mustExist is true, EvalSymlinks fully resolves the path;
// otherwise only the parent directory is resolved (for new-file creation).
func CheckPath(path string, cfg *Config, mustExist bool) (canonical string, errCode string) {
	cleaned := filepath.Clean(path)

	if mustExist {
		resolved, err := filepath.EvalSymlinks(cleaned)
		if err != nil {
			return "", ErrFileNotFound
		}
		canonical = resolved
	} else {
		// Resolve parent only; child may not exist yet.
		dir := filepath.Dir(cleaned)
		resolvedDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return "", ErrFileNotFound
		}
		canonical = filepath.Join(resolvedDir, filepath.Base(cleaned))
	}

	// The root directory itself must not be editable as a file.
	// Also enforces that canonical starts with "root/" rather than relying on
	// prefix matching to accidentally permit the root itself.
	root := cfg.Root + string(os.PathSeparator)
	if !strings.HasPrefix(canonical, root) {
		return "", ErrPathOutsideRoot
	}

	// Compute forward-slash relative path for doublestar matching.
	rel, _ := filepath.Rel(cfg.Root, canonical)
	rel = filepath.ToSlash(rel)

	if isBlocked(rel, cfg.BlockPatterns, cfg.AllowPatterns) {
		return "", ErrPathBlocked
	}

	return canonical, ""
}

// AcquireLock takes an exclusive non-blocking file lock for canonicalPath.
// The lock file lives in os.UserCacheDir()/lapp/locks/ — never in project
// directories. Returns an unlock function and an empty errCode on success.
func AcquireLock(canonicalPath string) (unlock func(), errCode string) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return nil, ErrLocked
	}

	lockDir := filepath.Join(cacheDir, "lapp", "locks")
	if err := os.MkdirAll(lockDir, 0700); err != nil {
		return nil, ErrLocked
	}

	// Use FNV-64a of the canonical path to get a stable, unique filename.
	h := fnv.New64a()
	h.Write([]byte(canonicalPath))
	lockPath := filepath.Join(lockDir, fmt.Sprintf("%x.lock", h.Sum64()))

	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, ErrLocked
	}

	if err := platformLock(int(f.Fd())); err != nil {
		f.Close()
		return nil, ErrLocked
	}

	unlock = func() {
		platformUnlock(int(f.Fd())) //nolint:errcheck
		f.Close()
		os.Remove(lockPath) // best-effort
	}
	return unlock, ""
}

// CleanupOrphans removes stale *.lapp.tmp files under root that are older than
// 5 minutes. Called on server startup per §9.1. Errors are ignored — best effort.
func CleanupOrphans(root string) {
	cutoff := 5 * time.Minute
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(d.Name(), ".lapp.tmp") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if time.Since(info.ModTime()) > cutoff {
			os.Remove(path) //nolint:errcheck
		}
		return nil
	})
}


// ─── helpers ─────────────────────────────────────────────────────────────────

// splitLines walks raw file bytes and returns lines and per-line terminators.
// A file ending with '\n' does NOT produce a trailing empty line.
func splitLines(data []byte) (lines []string, terminators []string) {
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] != '\n' {
			continue
		}
		term := "\n"
		lineEnd := i
		if i > 0 && data[i-1] == '\r' {
			term = "\r\n"
			lineEnd = i - 1
		}
		lines = append(lines, string(data[start:lineEnd]))
		terminators = append(terminators, term)
		start = i + 1
	}
	// Any content remaining after the last newline is a final unterminated line.
	if start < len(data) {
		lines = append(lines, string(data[start:]))
		terminators = append(terminators, "")
	}
	return
}

// isBlocked returns true if relPath matches a block pattern and is not
// explicitly allowed by an allow pattern.
func isBlocked(relPath string, blockPatterns, allowPatterns []string) bool {
	for _, allow := range allowPatterns {
		if match, _ := doublestar.Match(allow, relPath); match {
			return false
		}
	}
	for _, block := range blockPatterns {
		if match, _ := doublestar.Match(block, relPath); match {
			return true
		}
	}
	return false
}

// randomHex returns n random lowercase hex characters.
func RandomHex(n int) string {
	b := make([]byte, (n+1)/2)
	if _, err := rand.Read(b); err != nil {
		// Fallback: use process ID + a counter derived from address bits.
		return fmt.Sprintf("%016x", uintptr(os.Getpid()))[:n]
	}
	return hex.EncodeToString(b)[:n]
}
