package vfs

import (
	"bytes"
	"crypto/sha256"
	"sync"
	"sync/atomic"
	"testing"
)

// Concurrent Puts of the same block must not truncate each other's temp
// file: a reader must only ever see the whole block.
func TestCachePutConcurrent(t *testing.T) {
	data := bytes.Repeat([]byte("modalfs!"), blockSize/8)
	h := sha256.Sum256(data)
	for round := 0; round < 10; round++ {
		c, err := newBlockCache(t.TempDir(), 1<<30)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var done, bad atomic.Bool
		for range 8 {
			wg.Add(1)
			go func() { defer wg.Done(); c.Put(h[:], data) }()
		}
		var rg sync.WaitGroup
		for range 4 {
			rg.Add(1)
			go func() {
				defer rg.Done()
				for !done.Load() {
					if b := c.Get(h[:], len(data)); b != nil && !bytes.Equal(b, data) {
						bad.Store(true)
					}
				}
			}()
		}
		wg.Wait()
		done.Store(true)
		rg.Wait()
		if b := c.Get(h[:], len(data)); bad.Load() || !bytes.Equal(b, data) {
			t.Fatalf("round %d: a reader saw a partial block", round)
		}
	}
}

// A cached block of the wrong length is dropped, not returned.
func TestCacheGetShort(t *testing.T) {
	c, err := newBlockCache(t.TempDir(), 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte("abc"))
	c.Put(h[:], []byte("ab")) // as if the file had been damaged
	if b := c.Get(h[:], 3); b != nil || c.Has(h[:]) {
		t.Fatalf("short block returned (%q) or kept", b)
	}
}

// A cached block of the right length but damaged contents is dropped by
// Get, which writes and commits build on.
func TestCacheGetCorrupt(t *testing.T) {
	c, err := newBlockCache(t.TempDir(), 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte("abc"))
	c.Put(h[:], []byte("abd"))
	if b := c.Get(h[:], 3); b != nil || c.Has(h[:]) {
		t.Fatalf("damaged block returned (%q) or kept", b)
	}
	c.Put(h[:], []byte("abc"))
	if b := c.Get(h[:], 3); string(b) != "abc" {
		t.Fatalf("Get = %q", b)
	}
}

// ReadAt reads just a range, refuses one outside the block, and drops a
// block of the wrong length.
func TestCacheReadAt(t *testing.T) {
	c, err := newBlockCache(t.TempDir(), 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("0123456789")
	h := sha256.Sum256(data)
	c.Put(h[:], data)
	buf := make([]byte, 4)
	if !c.ReadAt(h[:], len(data), buf, 3) || string(buf) != "3456" {
		t.Fatalf("ReadAt = %q", buf)
	}
	if c.ReadAt(h[:], len(data), buf, 7) || c.ReadAt(h[:], len(data), buf, -1) {
		t.Fatal("ReadAt past the block's end succeeded")
	}
	if !c.Has(h[:]) {
		t.Fatal("a refused range dropped the block")
	}
	if c.ReadAt(h[:], len(data)+1, buf, 0) || c.Has(h[:]) {
		t.Fatal("block of the wrong length read or kept")
	}
}

// Read-ahead slots are shared and scale down with a small cache.
func TestCachePrefetchSlots(t *testing.T) {
	for _, tc := range []struct {
		max  int64
		want int
	}{{1 << 30, maxPrefetch}, {64 << 20, 2}, {0, 0}} {
		c, err := newBlockCache(t.TempDir(), tc.max)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for c.startPrefetch() {
			n++
		}
		if n != tc.want {
			t.Errorf("cache of %d MiB: %d slots, want %d", tc.max>>20, n, tc.want)
		}
		c.endPrefetch()
		if !c.startPrefetch() && tc.want > 0 {
			t.Errorf("a returned slot can't be taken again")
		}
	}
}
