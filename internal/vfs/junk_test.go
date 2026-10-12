package vfs

import (
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
