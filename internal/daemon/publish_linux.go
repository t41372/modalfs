package daemon

import "golang.org/x/sys/unix"

func publishRecovered(source, dest string) error {
	return unix.Renameat2(unix.AT_FDCWD, source, unix.AT_FDCWD, dest, unix.RENAME_NOREPLACE)
}
