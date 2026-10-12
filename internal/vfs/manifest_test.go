package vfs

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// A second open of a held manifest DB fails instead of hanging.
func TestManifestDBLocked(t *testing.T) {
	p := filepath.Join(t.TempDir(), "manifests.db")
	d, err := openManifestDB(p, "vo-1")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d2, err := openManifestDB(p, "vo-1"); err == nil {
		d2.Close()
		t.Fatal("second open succeeded")
	}
}

func hashOf(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// Hashes recorded with SetHash are seen at once, and written out on Close.
func TestManifestBatchedSetHash(t *testing.T) {
	p := filepath.Join(t.TempDir(), "manifests.db")
	d, err := openManifestDB(p, "vo-1")
	if err != nil {
		t.Fatal(err)
	}
	size := int64(3 * blockSize)
	d.SetHash("a", size, 7, 1, hashOf("b1"))
	if h := d.Hash("a", size, 7, 1); !bytes.Equal(h, hashOf("b1")) {
		t.Fatal("pending hash not visible")
	}
	if m := d.Get("a", size, 7); m == nil || !bytes.Equal(m.Hashes[1], hashOf("b1")) || m.Hashes[0] != nil {
		t.Fatalf("Get = %+v", m)
	}
	if d.Get("a", size, 8) != nil || d.Hash("a", size, 7, 3) != nil {
		t.Fatal("another version or a block past the end was found")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	d, err = openManifestDB(p, "vo-1")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if h := d.Hash("a", size, 7, 1); !bytes.Equal(h, hashOf("b1")) {
		t.Fatal("hash lost over Close")
	}
}

// Get returns a copy: changing it, or later SetHash calls, don't race.
func TestManifestGetCopy(t *testing.T) {
	d, err := openManifestDB(filepath.Join(t.TempDir(), "m.db"), "vo-1")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	size := int64(64 * blockSize)
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := g; i < 64; i += 4 {
				d.SetHash("f", size, 1, i, hashOf(fmt.Sprint(i)))
				if m := d.Get("f", size, 1); m != nil {
					for _, h := range m.Hashes {
						_ = len(h)
					}
					m.Hashes[0] = nil
				}
			}
		}(g)
	}
	wg.Wait()
	m := d.Get("f", size, 1)
	for i, h := range m.Hashes {
		if !bytes.Equal(h, hashOf(fmt.Sprint(i))) {
			t.Fatalf("block %d: hash lost", i)
		}
	}
}

// Manifests evicted from memory, pending or not, are still found in the DB;
// Delete and Rename see pending ones.
func TestManifestEvictDeleteRename(t *testing.T) {
	d, err := openManifestDB(filepath.Join(t.TempDir(), "m.db"), "vo-1")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.max = 1000 // a few small manifests
	for i := 0; i < 50; i++ {
		d.SetHash(fmt.Sprintf("d/f%d", i), 10, 1, 0, hashOf(fmt.Sprint(i)))
	}
	if d.lru.Len() >= 50 {
		t.Fatalf("%d manifests cached, want fewer", d.lru.Len())
	}
	for i := 0; i < 50; i++ {
		if h := d.Hash(fmt.Sprintf("d/f%d", i), 10, 1, 0); !bytes.Equal(h, hashOf(fmt.Sprint(i))) {
			t.Fatalf("f%d lost after eviction", i)
		}
	}
	d.SetHash("d/f1", 10, 1, 0, hashOf("new"))
	d.Rename("d", "e")
	if d.Hash("d/f1", 10, 1, 0) != nil || !bytes.Equal(d.Hash("e/f1", 10, 1, 0), hashOf("new")) {
		t.Fatal("rename lost a pending hash or left the old path")
	}
	d.SetHash("e/f2", 10, 1, 0, hashOf("x"))
	d.Delete("e/f2")
	d.mu.Lock()
	d.flushLocked()
	d.mu.Unlock()
	if d.Hash("e/f2", 10, 1, 0) != nil {
		t.Fatal("a deleted manifest came back")
	}
}

// An unreadable DB is moved aside and started afresh; a locked one isn't.
func TestManifestDBRebuilt(t *testing.T) {
	p := filepath.Join(t.TempDir(), "manifests.db")
	if err := os.WriteFile(p, bytes.Repeat([]byte("junk"), 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := openManifestDB(p, "vo-1")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if m, _ := filepath.Glob(p + ".broken-*"); len(m) != 1 {
		t.Fatalf("broken DB not kept aside: %v", m)
	}
}
