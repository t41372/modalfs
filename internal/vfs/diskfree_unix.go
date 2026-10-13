//go:build darwin || linux

package vfs

import "syscall"

// diskFree returns the bytes free to an unprivileged user on the disk holding dir, or -1 if unknown.
func diskFree(dir string) int64 {
	var s syscall.Statfs_t
	if err := syscall.Statfs(dir, &s); err != nil {
		return -1
	}
	return int64(s.Bavail) * int64(s.Bsize)
}
