//go:build windows

package fileio

import "golang.org/x/sys/windows"

func platformLock(fd int) error {
	ol := new(windows.Overlapped)
	return windows.LockFileEx(
		windows.Handle(fd),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, ol)
}

func platformUnlock(fd int) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(fd), 0, 1, 0, ol)
}
