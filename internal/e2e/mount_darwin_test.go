//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/modalfake"
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

// Fast cached reads must not shorten the timeout for the next slow read.
// The configured NFS timeout is a minute; a brief pause must not fail I/O.
func TestNFSReadWaitsForSlowServer(t *testing.T) {
	data := randomBytes(t, 64*MiB)
	e := setup(t, modalfake.Options{}, nil, func(c *modalapi.Client, volID string) {
		putFile(t, c, volID, "data.bin", data)
	})
	f, err := os.Open(filepath.Join(e.mnt, "data.bin"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := uncached(f); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64<<10)
	for i := range 32 {
		if _, err := f.ReadAt(buf, int64(i*len(buf))); err != nil {
			t.Fatal(err)
		}
	}

	// Resume from another process even if a blocked read stops this one.
	const pause = 12 * time.Second
	resume := exec.Command("/bin/sh", "-c", fmt.Sprintf("sleep %d; kill -CONT %d", int(pause/time.Second), e.proc.Pid))
	if err := resume.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resume.Wait() })
	if err := e.proc.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	const offset = 40 * MiB // beyond the kernel's own 16 MiB read-ahead
	start := time.Now()
	if n, err := f.ReadAt(buf, offset); err != nil || n != len(buf) {
		t.Fatalf("read after fast cached reads: %d bytes, %v", n, err)
	}
	if waited := time.Since(start); waited < pause/2 {
		t.Fatalf("read took only %v; it did not wait for the paused NFS server", waited)
	}
	if !bytes.Equal(buf, data[offset:offset+len(buf)]) {
		t.Fatal("read returned the wrong content after the server resumed")
	}
}
