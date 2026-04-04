---
id: lap-n4yz
status: closed
deps: [lap-c0wk]
links: []
created: 2026-04-04T04:16:55Z
type: task
priority: 2
assignee: Ronny Unger
tags: [wave-2, lapp]
---
# Issue 4: internal/fileio (I/O, locking, path security)

fileio owns all file system interactions — reading, writing, locking, path security, encoding detection. It is the safety boundary between the edit engine and the OS.

Key types and functions:

  type Config struct {
      Root          string
      BlockPatterns []string  // glob patterns (doublestar)
      AllowPatterns []string
      DefaultLimit  int
  }

  type FileData struct {
      Lines          []string
      Terminators    []string  // per-line: "\r\n", "\n", or "" for last
      MajorityEnding string
      HasBOM         bool
      Mode           fs.FileMode
      CanonicalPath  string
  }

Key function signatures:

  // ReadFile reads, validates, and parses a file. Returns FileData or error code string.
  func ReadFile(path string, cfg *Config) (*FileData, string)

  // WriteFile atomically writes lines back, preserving per-line terminators.
  // Temp file: <canonicalPath>.<pid>.<random>.lapp.tmp; chmod to original Mode before rename.
  func WriteFile(fd *FileData, newLines []string) string

  // AcquireLock acquires per-file advisory lock (platform impl in lock_*.go).
  // Lock file stored in os.UserCacheDir()/lapp/locks/<hash-of-path>.lock
  func AcquireLock(canonicalPath string) (unlock func(), errCode string)

PRE-MORTEM FIX pm-20260404-002: CheckPath takes mustExist bool.
filepath.EvalSymlinks errors on non-existent paths — lapp_write creates new files.

  func CheckPath(path string, cfg *Config, mustExist bool) (canonical string, errCode string) {
      cleaned := filepath.Clean(path)
      if mustExist {
          resolved, err := filepath.EvalSymlinks(cleaned)
          if err != nil { return "", ErrFileNotFound }
          canonical = resolved
      } else {
          dir := filepath.Dir(cleaned)
          resolvedDir, err := filepath.EvalSymlinks(dir)
          if err != nil { return "", ErrFileNotFound }
          canonical = filepath.Join(resolvedDir, filepath.Base(cleaned))
      }
      // verify canonical is under root, then check block list via doublestar.Match
  }

lapp_write calls CheckPath(path, cfg, false). All others call CheckPath(path, cfg, true).

Use github.com/bmatcuk/doublestar/v4 for ** glob patterns in block list.

Line ending detection (§9.4):
  crlfCount = bytes.Count(content, []byte("\r\n"))
  lfCount   = bytes.Count(content, []byte("\n")) - crlfCount
  majority  = "\r\n" if crlfCount > lfCount, else "\n"

BOM: strip \xEF\xBB\xBF from start, set HasBOM=true. BOM bytes never included in hash.
Binary detection: bytes.IndexByte(first8192, 0) >= 0 → ERR_BINARY_FILE.
Atomic write sequence (§9.1) — exact steps in WriteFile:
1. filepath.EvalSymlinks → canonical path
2. os.Stat(canonical) → capture info.Mode()
3. Write to temp file <canonicalPath>.<pid>.<random>.lapp.tmp with permissions 0600 explicitly
4. os.Chmod(tempPath, info.Mode()) — restore original permissions BEFORE rename
5. os.Rename(tempPath, canonicalPath) — atomic on POSIX; targets canonical (not symlink)
6. On any error: attempt os.Remove(tempPath) — best-effort, log but don't propagate failure

Windows-specific: if Rename fails because another process has the file open, surface as:
  "Cannot write: another process has the file open. Close it in your editor and retry."

Orphan cleanup (§9.1): spec says scan for *.lapp.tmp older than 5 minutes on startup.
DEFERRED to v1.1 per pre-mortem pm-20260404-007 — skip in this implementation.

Locking behavior (§9.2): lapp_edit acquires advisory lock before reading for hash verification.
lapp_read does NOT lock. Operations on different files are fully parallel.
Lock files in os.UserCacheDir()/lapp/locks/<hash-of-path>.lock; unlink after release (best-effort).

lock_unix.go (//go:build !windows):
  import "golang.org/x/sys/unix"
  func platformLock(fd int) error   { return unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) }
  func platformUnlock(fd int) error { return unix.Flock(fd, unix.LOCK_UN) }

lock_windows.go (//go:build windows):
  import "golang.org/x/sys/windows"
  // LockFileEx with LOCKFILE_EXCLUSIVE_LOCK|LOCKFILE_FAIL_IMMEDIATELY

Complete default block list (§9.8) — match against canonical path relative to root:
  **/.env
  **/.env.*            (but NOT **/.env.example or **/.env.sample — these are safe)
  **/secrets.*
  **/credentials.*
  **/*.pem
  **/*.key
  **/*.p12
  **/*.pfx
  **/.aws/credentials
  **/.aws/config

Do NOT use syscall.Flock directly.

## Tests (internal/fileio/fileio_test.go)

- TestReadFile_Binary: Null byte → ERR_BINARY_FILE
- TestReadFile_InvalidUTF8: Bad bytes → ERR_INVALID_ENCODING
- TestReadFile_CRLF: Returns lines with CRLF terminators; majority = CRLF
- TestReadFile_BOM: BOM stripped; HasBOM=true; line 1 hash unaffected by BOM
- TestWriteFile_PreservesTerminators: CRLF file written back with CRLF
- TestWriteFile_NewLinesUseMajority: Inserted lines get majority terminator
- TestWriteFile_AtomicOnInterrupt: Original intact if interrupted mid-write
- TestCheckPath_OutsideRoot: ERR_PATH_OUTSIDE_ROOT
- TestCheckPath_SymlinkEscape: Symlink pointing outside root → ERR_PATH_OUTSIDE_ROOT
- TestCheckPath_Blocked: .env → ERR_PATH_BLOCKED
- TestLock_Serializes: Two goroutines writing same file → second gets ERR_LOCKED
- TestLock_FileInCacheDir: Lock file not in project tree
- TestLock_UnlinkedAfterRelease: Lock file removed after edit completes
- TestWriteFile_SymlinkPreserved: After edit, symlink still points to original target file; file contents updated

## Acceptance Criteria

All 14 named test functions pass; lock files in os.UserCacheDir(); `GOOS=windows go build ./internal/fileio` succeeds

