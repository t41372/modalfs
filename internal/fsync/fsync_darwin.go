package fsync

import (
	"errors"
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

// On macOS, os.File.Sync calls fcntl(F_FULLFSYNC) through a runtime call
// that doesn't tell the scheduler it may block (Go 1.27). While it waits on
// the disk, or on an NFS server, the next stop-the-world (every GC) can't
// finish, and every goroutine in the process stops with it. In modalfs that
// stalls the whole mount for as long as one sync takes; in a test that
// syncs a file on a mount served through its own process (the fake Modal,
// the proxy), nothing ever wakes up again. x/sys/unix makes the same call
// as a proper system call.
func file(f *os.File) error {
	fd := f.Fd()
	_, err := unix.FcntlInt(fd, unix.F_FULLFSYNC, 0)
	if errors.Is(err, unix.ENOTSUP) { // some filesystems (SMB) don't support it
		err = unix.Fsync(int(fd))
	}
	runtime.KeepAlive(f)
	if err != nil {
		return &os.PathError{Op: "sync", Path: f.Name(), Err: err}
	}
	return nil
}
