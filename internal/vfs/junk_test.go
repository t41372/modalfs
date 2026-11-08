package vfs

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Local-only files are capped; the least recently used go first, and one
// used recently is kept even over the cap.
func TestEphTrim(t *testing.T) {
	fs := &FS{opt: Options{FilterJunk: true}, eph: map[string]*ephFile{}}
	for i := 0; i < ephMaxFiles; i++ {
		fs.ephWrite(fmt.Sprintf("d/._f%d", i), []byte("xattr"), 0)
	}
	if len(fs.eph) != ephMaxFiles {
		t.Fatalf("%d files kept before the cap was reached", len(fs.eph))
	}
	old := time.Now().Add(-2 * ephKeep)
	for p, e := range fs.eph {
		if p != "d/._f7" {
			e.used = old
		}
	}
	fs.ephWrite("d/.DS_Store", make([]byte, 100), 0)
	if n := len(fs.eph); n > ephMaxFiles*9/10 {
		t.Fatalf("%d files kept after going over the cap", n)
	}
	if _, ok := fs.ephStat("d/._f7"); !ok {
		t.Fatal("a recently used file was dropped")
	}
	if _, ok := fs.ephStat("d/.DS_Store"); !ok {
		t.Fatal("the file just written was dropped")
	}
	var total int64
	for _, e := range fs.eph {
		total += int64(len(e.data))
	}
	if total != fs.ephBytes {
		t.Fatalf("byte count %d, files hold %d", fs.ephBytes, total)
	}
	// all in use: nothing is dropped
	for i := 0; i < 2*ephMaxFiles; i++ {
		fs.ephWrite(fmt.Sprintf("e/._f%d", i), nil, 0)
	}
	if len(fs.eph) < 2*ephMaxFiles {
		t.Fatalf("files in use were dropped: %d left", len(fs.eph))
	}
}

// With the filter on, a truncate of a metadata file that exists on Modal
// changes a local-only copy, as a write to it does; the file on Modal stays.
func TestTruncateOfRemoteJunkStaysLocal(t *testing.T) {
	f := newFakeFS(t, Options{WriteBack: time.Hour, MaxPending: -1, FilterJunk: true})
	f.put(t, "._x", []byte("EXISTING"))
	if err := f.Truncate("._x", 1); err != nil {
		t.Fatal(err)
	}
	if a, err := f.Stat("._x"); err != nil || a.Size != 1 {
		t.Fatalf("the mount shows %+v, %v; want a 1-byte file", a, err)
	}
	if got, err := f.client.ReadFile(context.Background(), f.vol.ID, "._x", 1<<20); err != nil || string(got) != "EXISTING" {
		t.Fatalf("on Modal: %q, %v; want it untouched", got, err)
	}
}
