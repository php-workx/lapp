//go:build windows

package fileio

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// renameAtomic renames src to dst on Windows via MoveFileExW.
// Detects sharing violations (another process has the file open) and returns
// an actionable message per §9.1 rather than a raw OS error string.
func renameAtomic(src, dst string) string {
	if err := os.Rename(src, dst); err != nil {
		var winErr *os.PathError
		if errors.As(err, &winErr) {
			if errno, ok := winErr.Err.(windows.Errno); ok {
				if errno == windows.ERROR_SHARING_VIOLATION {
					return "Cannot write: another process has the file open. Close it in your editor and retry."
				}
			}
		}
		if os.IsPermission(err) {
			return ErrPermissionDenied
		}
		return ErrWriteFailed
	}
	return ""
}
