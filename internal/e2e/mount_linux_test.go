//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"strings"

	"golang.org/x/sys/unix"
)

// Linux mounts modalfs over FUSE, without root, through fusermount3.

// /proc/self/mounts writes these characters of a path as octal escapes.
var mountsEscape = strings.NewReplacer(`\`, `\134`, " ", `\040`, "\t", `\011`, "\n", `\012`)

func mounted(mnt string) bool {
	b, _ := os.ReadFile("/proc/self/mounts")
	want := mountsEscape.Replace(mnt)
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) > 1 && f[1] == want {
			return true
		}
	}
	return false
}

// forceUnmount takes a mount off whether or not modalfs still serves it.
// Lazily (-z), so a file still open on it doesn't stop the unmount.
func forceUnmount(mnt string) error {
	return exec.Command("fusermount3", "-u", "-z", mnt).Run()
}

// uncached drops what the kernel has cached of f, so reading it once more
// reaches modalfs.
func uncached(f *os.File) error {
	return unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED)
}
