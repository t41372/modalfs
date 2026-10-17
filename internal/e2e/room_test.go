//go:build e2e

package e2e

import (
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/fsync"
	"github.com/t41372/modalfs/internal/modalfake"
)

// The cache size is all the disk modalfs uses: changes not on Modal yet
// count toward it too. These tests copy a file several times that size over
// a slow uplink.

// diskUse sums the files under dir (block files count at their full size).
func diskUse(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// watchDisk samples diskUse(dir) every 100 ms and returns the most it saw when stopped.
func watchDisk(dir string) func() int64 {
	var most atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			if n := diskUse(dir); n > most.Load() {
				most.Store(n)
			}
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()
	return func() int64 {
		close(stop)
		wg.Wait()
		return most.Load()
	}
}

// writeTimed writes data to name in 4 MiB writes, syncing after each when
// sync is set, and returns the slowest write.
func (e *env) writeTimed(name string, data []byte, sync bool) time.Duration {
	e.t.Helper()
	f, err := os.Create(filepath.Join(e.mnt, name))
	if err != nil {
		e.t.Fatal(err)
	}
	var worst time.Duration
	for off := 0; off < len(data); off += 4 * MiB {
		t0 := time.Now()
		if _, err := f.Write(data[off:min(off+4*MiB, len(data))]); err != nil {
			e.t.Fatalf("write at %d MiB: %v", off/MiB, err)
		}
		if sync {
			if err := fsync.File(f); err != nil {
				e.t.Fatalf("sync at %d MiB: %v", off/MiB, err)
			}
		}
		worst = max(worst, time.Since(t0))
	}
	if err := fsync.File(f); err != nil {
		e.t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		e.t.Fatal(err)
	}
	return worst
}

// A copy four times the limit finishes, at the speed of the upload, without
// modalfs taking more disk than the limit; no write waits anywhere near the
// minute after which macOS gives up on the mount; and listings keep
// answering meanwhile. modalfs holds a write for a block or two's upload,
// but once writes wait, the macOS NFS client can hold the copy's writes
// back by itself for up to about 30 s. (A stat of the file being copied
// can wait as long as a write: the NFS client sends the file's writes
// before it asks for its size.)
func TestCopyLargerThanTheLimit(t *testing.T) {
	const limit = 256 * MiB
	e := setup(t, modalfake.Options{UpBytesPerSec: 20 * MiB, Latency: 20 * time.Millisecond}, []string{"-cache-size", "256M"})
	data := randomBytes(t, 1024*MiB)
	disk := watchDisk(e.cache)
	listing := e.probe("")
	stat := e.probe("big.bin")
	t0 := time.Now()
	slowest := e.writeTimed("big.bin", data, false)
	t.Logf("copied %d MiB in %v; slowest 4 MiB write took %v", len(data)/MiB, time.Since(t0).Round(time.Second), slowest)
	e.waitRemote("big.bin", data, 3*time.Minute)
	most := disk()
	worst, worstStat := listing(), stat()
	t.Logf("most disk used: %d MiB (limit %d MiB); slowest listing: %v; slowest listing and stat of the file: %v", most/MiB, limit/MiB, worst, worstStat)
	// Writes in flight when room runs out may each add a block past it.
	if most > limit+64*MiB {
		t.Errorf("modalfs used %d MiB of disk, limit %d MiB", most/MiB, limit/MiB)
	}
	if slowest > maxHeld {
		t.Errorf("a write waited %v for room", slowest)
	}
	if worst > maxStall {
		t.Errorf("listing the mount took %v while writes waited for room (limit %v)", worst, maxStall)
	}
	if worstStat > maxHeld {
		t.Errorf("a stat of the file being copied took %v", worstStat)
	}
}

// Blocks a commit put on Modal are let go while the file is still being
// written. If modalfs is killed then, the next start must still commit the
// file exactly: a block it let go comes from the version already on Modal.
func TestCrashAfterBlocksWereLetGo(t *testing.T) {
	e := setup(t, modalfake.Options{UpBytesPerSec: 10 * MiB}, []string{"-cache-size", "256M"})
	data := randomBytes(t, 400*MiB)
	// every write is durable before the next, so all of it must survive
	e.writeTimed("crash.bin", data, true)
	if n := diskUse(e.cache); n >= int64(len(data)) {
		t.Fatalf("modalfs holds %d MiB for a %d MiB file: nothing was let go", n/MiB, len(data)/MiB)
	}
	e.crash()
	e.launch()
	e.waitRemote("crash.bin", data, 3*time.Minute)
}
