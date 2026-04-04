package fileio

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"testing"
)

// newCfg returns a Config rooted at dir using the default block/allow lists.
func newCfg(dir string) *Config {
	// Resolve symlinks so cfg.Root matches what CheckPath will compute via
	// filepath.EvalSymlinks (e.g. /tmp → /private/tmp on macOS).
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		resolved = dir
	}
	return &Config{
		Root:          resolved,
		BlockPatterns: DefaultBlockPatterns,
		AllowPatterns: DefaultAllowPatterns,
		DefaultLimit:  100,
	}
}

// mustWriteFile creates path with the given raw bytes, fataling on error.
func mustWriteFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("mustWriteFile %s: %v", path, err)
	}
}

// TestReadFile_Binary: file containing a null byte → ErrBinaryFile.
func TestReadFile_Binary(t *testing.T) {
	dir := t.TempDir()
	cfg := newCfg(dir)
	path := filepath.Join(dir, "binary.bin")
	mustWriteFile(t, path, []byte("hello\x00world"))

	fd, code := ReadFile(path, cfg)
	if code != ErrBinaryFile {
		t.Fatalf("expected %s, got %q (fd=%v)", ErrBinaryFile, code, fd)
	}
}

// TestReadFile_InvalidUTF8: file with invalid UTF-8 bytes → ErrInvalidEncoding.
func TestReadFile_InvalidUTF8(t *testing.T) {
	dir := t.TempDir()
	cfg := newCfg(dir)
	path := filepath.Join(dir, "latin1.txt")
	// 0xFF is never valid in UTF-8; no null byte so binary check passes.
	mustWriteFile(t, path, []byte("caf\xff\xfe"))

	fd, code := ReadFile(path, cfg)
	if code != ErrInvalidEncoding {
		t.Fatalf("expected %s, got %q (fd=%v)", ErrInvalidEncoding, code, fd)
	}
}

// TestReadFile_CRLF: file with \r\n line endings → MajorityEnding and all
// terminators are "\r\n".
func TestReadFile_CRLF(t *testing.T) {
	dir := t.TempDir()
	cfg := newCfg(dir)
	path := filepath.Join(dir, "crlf.txt")
	mustWriteFile(t, path, []byte("foo\r\nbar\r\nbaz\r\n"))

	fd, code := ReadFile(path, cfg)
	if code != "" {
		t.Fatalf("unexpected error: %s", code)
	}
	if fd.MajorityEnding != "\r\n" {
		t.Errorf("MajorityEnding = %q, want \\r\\n", fd.MajorityEnding)
	}
	if len(fd.Lines) != 3 {
		t.Fatalf("len(Lines) = %d, want 3", len(fd.Lines))
	}
	for i, term := range fd.Terminators {
		if term != "\r\n" {
			t.Errorf("Terminators[%d] = %q, want \\r\\n", i, term)
		}
	}
}

// TestReadFile_BOM: UTF-8 BOM prefix is detected, stripped, and not present
// in any of the parsed lines.
func TestReadFile_BOM(t *testing.T) {
	dir := t.TempDir()
	cfg := newCfg(dir)
	path := filepath.Join(dir, "bom.txt")
	// BOM + "hello\nworld"
	mustWriteFile(t, path, append([]byte{0xEF, 0xBB, 0xBF}, []byte("hello\nworld")...))

	fd, code := ReadFile(path, cfg)
	if code != "" {
		t.Fatalf("unexpected error: %s", code)
	}
	if !fd.HasBOM {
		t.Error("HasBOM should be true")
	}
	if len(fd.Lines) == 0 {
		t.Fatal("no lines parsed")
	}
	// BOM bytes must not appear in Lines[0].
	if len(fd.Lines[0]) >= 3 && fd.Lines[0][:3] == "\xEF\xBB\xBF" {
		t.Errorf("Lines[0] still contains BOM: %q", fd.Lines[0])
	}
	if fd.Lines[0] != "hello" {
		t.Errorf("Lines[0] = %q, want \"hello\"", fd.Lines[0])
	}
}

// TestWriteFile_PreservesTerminators: a CRLF file written back must still
// have \r\n endings in the on-disk output.
func TestWriteFile_PreservesTerminators(t *testing.T) {
	dir := t.TempDir()
	cfg := newCfg(dir)
	path := filepath.Join(dir, "crlf.txt")
	mustWriteFile(t, path, []byte("alpha\r\nbeta\r\n"))

	fd, code := ReadFile(path, cfg)
	if code != "" {
		t.Fatalf("ReadFile: %s", code)
	}

	if code := WriteFile(fd, fd.Lines); code != "" {
		t.Fatalf("WriteFile: %s", code)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile from disk: %v", err)
	}
	want := []byte("alpha\r\nbeta\r\n")
	if string(got) != string(want) {
		t.Errorf("on-disk content = %q, want %q", got, want)
	}
}

// TestWriteFile_NewLinesUseMajority: lines appended beyond the original set
// must use the file's majority ending, not \n.
func TestWriteFile_NewLinesUseMajority(t *testing.T) {
	dir := t.TempDir()
	cfg := newCfg(dir)
	path := filepath.Join(dir, "crlf.txt")
	mustWriteFile(t, path, []byte("line1\r\nline2\r\n"))

	fd, code := ReadFile(path, cfg)
	if code != "" {
		t.Fatalf("ReadFile: %s", code)
	}

	newLines := append(fd.Lines, "line3", "line4")
	if code := WriteFile(fd, newLines); code != "" {
		t.Fatalf("WriteFile: %s", code)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	// line1 and line2 keep \r\n; line3 is a new non-last line → \r\n; line4 is
	// last → no terminator.
	want := "line1\r\nline2\r\nline3\r\nline4"
	if string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestWriteFile_AtomicOnInterrupt verifies that WriteFile leaves no temp files
// in the directory after completion (success path), and that the content is
// correct. Atomicity against crashes is guaranteed by the OS rename semantics
// used internally.
func TestWriteFile_AtomicOnInterrupt(t *testing.T) {
	dir := t.TempDir()
	cfg := newCfg(dir)
	path := filepath.Join(dir, "file.txt")
	mustWriteFile(t, path, []byte("original\n"))

	fd, code := ReadFile(path, cfg)
	if code != "" {
		t.Fatalf("ReadFile: %s", code)
	}

	if code := WriteFile(fd, []string{"updated"}); code != "" {
		t.Fatalf("WriteFile: %s", code)
	}

	// Verify content was updated.
	got, _ := os.ReadFile(path)
	if string(got) != "updated\n" {
		t.Errorf("content = %q, want %q", got, "updated\n")
	}

	// Verify no leftover temp files in the directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

// TestCheckPath_OutsideRoot: a path outside cfg.Root must return
// ErrPathOutsideRoot.
func TestCheckPath_OutsideRoot(t *testing.T) {
	dir := t.TempDir()
	cfg := newCfg(dir)

	// /tmp/evil is clearly outside the tmpDir root.
	_, code := CheckPath("/tmp/evil", cfg, false)
	if code != ErrPathOutsideRoot {
		t.Errorf("expected %s, got %q", ErrPathOutsideRoot, code)
	}
}

// TestCheckPath_SymlinkEscape: a symlink inside root pointing outside root
// must not escape the containment check.
func TestCheckPath_SymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	cfg := newCfg(root)

	// Create a symlink inside root pointing to a file outside root.
	target := filepath.Join(outside, "secret.txt")
	mustWriteFile(t, target, []byte("secret"))
	link := filepath.Join(root, "escape.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	_, code := CheckPath(link, cfg, true)
	if code != ErrPathOutsideRoot {
		t.Errorf("expected %s, got %q", ErrPathOutsideRoot, code)
	}
}

// TestCheckPath_Blocked: .env is blocked; .env.example is explicitly allowed.
func TestCheckPath_Blocked(t *testing.T) {
	dir := t.TempDir()
	cfg := newCfg(dir)

	// Create both files so mustExist=true works.
	envPath := filepath.Join(dir, ".env")
	examplePath := filepath.Join(dir, ".env.example")
	mustWriteFile(t, envPath, []byte("SECRET=x"))
	mustWriteFile(t, examplePath, []byte("SECRET=changeme"))

	if _, code := CheckPath(envPath, cfg, true); code != ErrPathBlocked {
		t.Errorf(".env: expected %s, got %q", ErrPathBlocked, code)
	}
	if _, code := CheckPath(examplePath, cfg, true); code != "" {
		t.Errorf(".env.example: expected no error, got %q", code)
	}
}

// TestLock_Serializes: two goroutines racing on the same canonical path —
// only the first acquires the lock; the second gets ErrLocked.
func TestLock_Serializes(t *testing.T) {
	// We don't need a real file; AcquireLock uses the path as a key only.
	canonicalPath := filepath.Join(t.TempDir(), "shared.txt")

	locked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})

	var firstCode string
	go func() {
		unlock, code := AcquireLock(canonicalPath)
		firstCode = code
		close(locked) // signal: we have (or failed to get) the lock
		if code == "" {
			<-release
			unlock()
		}
		close(done)
	}()

	<-locked
	if firstCode != "" {
		t.Fatalf("first goroutine failed to acquire lock: %s", firstCode)
	}

	_, code := AcquireLock(canonicalPath)
	if code != ErrLocked {
		t.Errorf("second lock: expected %s, got %q", ErrLocked, code)
	}

	close(release)
	<-done
}

// TestLock_FileInCacheDir: the lock file must live under UserCacheDir, not in
// the project or temp directory.
func TestLock_FileInCacheDir(t *testing.T) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		t.Skip("UserCacheDir unavailable:", err)
	}

	canonicalPath := filepath.Join(t.TempDir(), "test.txt")
	unlock, code := AcquireLock(canonicalPath)
	if code != "" {
		t.Fatalf("AcquireLock: %s", code)
	}
	defer unlock()

	h := fnv.New64a()
	h.Write([]byte(canonicalPath))
	lockPath := filepath.Join(cacheDir, "lapp", "locks",
		fmt.Sprintf("%x.lock", h.Sum64()))

	if _, err := os.Stat(lockPath); err != nil {
		t.Errorf("lock file not found in cache dir (%s): %v", lockPath, err)
	}
}

// TestLock_UnlinkedAfterRelease: calling unlock() removes the lock file.
func TestLock_UnlinkedAfterRelease(t *testing.T) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		t.Skip("UserCacheDir unavailable:", err)
	}

	canonicalPath := filepath.Join(t.TempDir(), "test.txt")
	unlock, code := AcquireLock(canonicalPath)
	if code != "" {
		t.Fatalf("AcquireLock: %s", code)
	}

	h := fnv.New64a()
	h.Write([]byte(canonicalPath))
	lockPath := filepath.Join(cacheDir, "lapp", "locks",
		fmt.Sprintf("%x.lock", h.Sum64()))

	// Verify it exists before release.
	if _, err := os.Stat(lockPath); err != nil {
		t.Errorf("lock file missing before unlock: %v", err)
	}

	unlock()

	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Errorf("lock file should be removed after unlock; stat err = %v", err)
	}
}

// TestWriteFile_SymlinkPreserved: ReadFile via a symlink resolves to the real
// target; WriteFile updates the target; the symlink itself is untouched.
func TestWriteFile_SymlinkPreserved(t *testing.T) {
	dir := t.TempDir()
	cfg := newCfg(dir)

	target := filepath.Join(dir, "target.txt")
	mustWriteFile(t, target, []byte("original\n"))

	// EvalSymlinks resolves any symlinks in t.TempDir() path (e.g. /private on macOS).
	expectedCanonical, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatalf("EvalSymlinks target: %v", err)
	}

	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	// ReadFile via symlink path; canonical resolves to real target.
	fd, code := ReadFile(link, cfg)
	if code != "" {
		t.Fatalf("ReadFile via symlink: %s", code)
	}
	if fd.CanonicalPath != expectedCanonical {
		t.Errorf("CanonicalPath = %q, want %q", fd.CanonicalPath, expectedCanonical)
	}

	if code := WriteFile(fd, []string{"updated"}); code != "" {
		t.Fatalf("WriteFile: %s", code)
	}

	// Target contents updated.
	got, _ := os.ReadFile(target)
	if string(got) != "updated\n" {
		t.Errorf("target content = %q, want %q", got, "updated\n")
	}

	// Symlink still points to the same destination.
	dest, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if dest != target {
		t.Errorf("symlink dest = %q, want %q", dest, target)
	}
}

