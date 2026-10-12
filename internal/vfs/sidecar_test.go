package vfs

import (
	"testing"

	"github.com/t41372/modalfs/internal/modalapi"
)

// A sidecar is used only if it agrees with the block hashes already known.
func TestSidecarChecked(t *testing.T) {
	f := newFakeFS(t, Options{ReadAhead: -1, SidecarMin: 1})
	data := randBytes(2*blockSize + 10)
	f.put(t, "big", data)
	a, err := f.Stat("big")
	if err != nil {
		t.Fatal(err)
	}
	size, mtime := a.Size, a.Mtime.Unix()
	good := &manifest{Size: size, Mtime: mtime}
	good.Hashes = append(good.Hashes, modalapi.HashBlocks(data)...)
	bad := good.clone()
	bad.Hashes[0] = hashOf("not block 0")

	// nothing known yet: size and mtime are all there is to check
	if err := f.writeSidecar("big", bad); err != nil {
		t.Fatal(err)
	}
	if f.loadSidecar("big", size, mtime) == nil {
		t.Fatal("unverifiable sidecar refused")
	}
	f.mdb.Delete("big")

	// block 0 read, so its hash is known
	if _, err := f.ReadAt("big", make([]byte, 10), 0); err != nil {
		t.Fatal(err)
	}
	if f.loadSidecar("big", size, mtime) != nil {
		t.Fatal("sidecar with a wrong hash for a known block was trusted")
	}
	if h := f.mdb.Hash("big", size, mtime, 0); h == nil || string(h) == string(bad.Hashes[0]) {
		t.Fatal("a refused sidecar changed the known hashes")
	}
	if err := f.writeSidecar("big", good); err != nil {
		t.Fatal(err)
	}
	if m := f.loadSidecar("big", size, mtime); m == nil || f.mdb.Hash("big", size, mtime, 2) == nil {
		t.Fatal("a matching sidecar wasn't used")
	}
}
