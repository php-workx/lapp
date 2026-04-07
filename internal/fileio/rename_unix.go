//go:build !windows

package fileio

import "os"

// RenameAtomic renames src to dst. On POSIX this is guaranteed atomic.
// Returns an empty string on success, or an error code on failure.
func RenameAtomic(src, dst string) string {
	if err := os.Rename(src, dst); err != nil {
		if os.IsPermission(err) {
			return ErrPermissionDenied
		}
		return ErrWriteFailed
	}
	return ""
}
