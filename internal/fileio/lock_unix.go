//go:build !windows

package fileio

import "golang.org/x/sys/unix"

func platformLock(fd int) error {
	return unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
}

func platformUnlock(fd int) error {
	return unix.Flock(fd, unix.LOCK_UN)
}
