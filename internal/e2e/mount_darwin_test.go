//go:build e2e

package e2e

import (
	"bytes"
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

// macOS mounts modalfs over its NFS client.

func mounted(mnt string) bool {
	out, _ := exec.Command("/sbin/mount").Output()
	return bytes.Contains(out, []byte(" "+mnt+" ")) || bytes.Contains(out, []byte("/private"+mnt+" "))
}

// forceUnmount takes a mount off whether or not modalfs still serves it.
func forceUnmount(mnt string) error {
	return exec.Command("/sbin/umount", "-f", mnt).Run()
}

// uncached makes reads of f bypass the NFS client's own cache, so every
// read reaches modalfs.
func uncached(f *os.File) error {
	_, err := unix.FcntlInt(f.Fd(), unix.F_NOCACHE, 1)
	return err
}
