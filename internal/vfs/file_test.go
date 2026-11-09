package vfs

import (
	"bytes"
	"crypto/sha256"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/volume"
)

func testFS(t *testing.T, cacheDir string) *FS {
	return testFSLimit(t, cacheDir, 0)
}

// testFSLimit is testFS with a limit on the disk modalfs may use (0: none).
func testFSLimit(t *testing.T, cacheDir string, limit int64) *FS {
	bc, err := newBlockCache(filepath.Join(t.TempDir(), "blocks"), limit)
	if err != nil {
		t.Fatal(err)
	}
	// No commit within a test: there is no client to commit to.
	fs := &FS{opt: Options{CacheDir: cacheDir, WriteBack: time.Hour, MaxPending: -1}, log: slog.New(slog.DiscardHandler), files: map[string]*fileState{}, cache: bc}
	return fs
}

// Writes and truncates of a new file stay local and keep full-size blocks.
func TestFileWriteTruncate(t *testing.T) {
	st := newFileState(testFS(t, t.TempDir()), "a", "1", false, modalapi.Entry{})
	data := bytes.Repeat([]byte{7}, 100)
	if _, err := st.writeAt(data, blockSize-50); err != nil {
		t.Fatal(err)
	}
	if err := st.truncate(blockSize - 10); err != nil {
		t.Fatal(err)
	}
	if err := st.truncate(blockSize + 50); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 100)
	if _, err := st.readAt(got, blockSize-50); err != nil {
		t.Fatal(err)
	}
	want := append(bytes.Repeat([]byte{7}, 40), make([]byte, 60)...)
	if !bytes.Equal(got, want) {
		t.Fatalf("read back %v", got)
	}
	ents, _ := os.ReadDir(st.dir)
	for _, e := range ents {
		if info, _ := e.Info(); e.Name() != journalName && info.Size() != blockSize {
			t.Errorf("%s is %d bytes", e.Name(), info.Size())
		}
	}
}

// A failed first write to an existing file leaves no state behind.
func TestFileFailedWriteDetaches(t *testing.T) {
	notDir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notDir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fs := testFS(t, notDir) // block files can't be created under it
	st := newFileState(fs, "a", "1", true, modalapi.Entry{Path: "a"})
	fs.files["a"] = st
	if _, err := st.writeAt([]byte("x"), 0); err == nil {
		t.Fatal("write succeeded")
	}
	if fs.files["a"] != nil || !st.removed {
		t.Fatal("failed write left the file state attached")
	}
}

// The cache counts the disk each file's pending changes take, and drops
// them from the count when they go.
func TestPendingIsCounted(t *testing.T) {
	fs := testFS(t, t.TempDir())
	st := newFileState(fs, "a", "1", false, modalapi.Entry{})
	fs.files["a"] = st
	if _, err := st.writeAt([]byte("x"), 2*blockSize+5); err != nil {
		t.Fatal(err)
	}
	if got := fs.cache.Pending(); got != blockSize {
		t.Fatalf("pending = %d after writing one block, want %d", got, blockSize)
	}
	if _, err := st.writeAt(bytes.Repeat([]byte{1}, blockSize), 0); err != nil {
		t.Fatal(err)
	}
	if got := fs.cache.Pending(); got != 2*blockSize {
		t.Fatalf("pending = %d after writing a second block, want %d", got, 2*blockSize)
	}
	if err := st.truncate(10); err != nil {
		t.Fatal(err)
	}
	if got := fs.cache.Pending(); got != blockSize {
		t.Fatalf("pending = %d after truncating to one block, want %d", got, blockSize)
	}
	st.discard()
	if got := fs.cache.Pending(); got != 0 {
		t.Fatalf("pending = %d after discarding, want 0", got)
	}
}

// Pending changes may take the limit but the share kept for reading; past
// it there's room only once some are uploaded, and none at all when there's
// nothing to upload. Room given is counted at once, so writes asking at
// the same time can't all be given the same room.
func TestRoomForPending(t *testing.T) {
	const limit = 64 * blockSize
	c, err := newBlockCache(t.TempDir(), limit)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _, _, _ := c.room(blockSize); !ok {
		t.Fatal("no room in an empty cache")
	}
	if got := c.Pending(); got != blockSize {
		t.Fatalf("pending = %d after room for a block was given, want %d: room given must count until the write makes its blocks", got, blockSize)
	}
	c.addPending(limit - readRoom(limit) - blockSize)
	ok, freed, stuck, _ := c.room(blockSize)
	if ok || stuck {
		t.Fatalf("full: ok=%v stuck=%v, want a wait", ok, stuck)
	}
	c.addPending(-blockSize)
	select {
	case <-freed:
	default:
		t.Fatal("uploading a block didn't wake the waiting writes")
	}
	if ok, _, _, _ := c.room(blockSize); !ok {
		t.Fatal("no room after a block was uploaded")
	}
	c.addPending(-(limit - readRoom(limit)))
	if ok, _, stuck, _ := c.room(limit); ok || !stuck {
		t.Fatalf("a write bigger than the limit with nothing pending: ok=%v stuck=%v, want stuck", ok, stuck)
	}
}

// A write's room is the blocks it makes dirty, the copies it keeps for the
// commit in flight, and the old partial last block it makes whole when it
// starts past it.
func TestRoomCountsWhatAWriteMakes(t *testing.T) {
	// on Modal: a whole block 0, changed here, and a byte of block 1
	st := newFileState(testFS(t, t.TempDir()), "a", "1", true, modalapi.Entry{Path: "a", Size: blockSize + 1})
	st.dirty[0] = true
	for _, c := range []struct {
		name   string
		off, n int64
		blocks int64
	}{
		{"into a dirty block", 1, 10, 0},
		{"into the partial last block", blockSize + 5, 10, 1},
		{"across two blocks, one dirty", blockSize - 5, 10, 1},
		{"past the partial last block", 3*blockSize + 5, 1, 2},
		{"from the partial last block on", blockSize + 5, 2*blockSize - 10, 2},
	} {
		if got := st.roomLocked(c.off, c.n); got != c.blocks*blockSize {
			t.Errorf("%s: room %d blocks, want %d", c.name, got/blockSize, c.blocks)
		}
	}
	st.snap = &snapshot{dirty: map[int64]bool{0: true}, saved: map[int64]bool{}}
	if got := st.roomLocked(1, 10); got != blockSize {
		t.Errorf("into a block the commit in flight still reads: room %d blocks, want 1 for its copy", got/blockSize)
	}
	st.snap.saved[0] = true
	if got := st.roomLocked(1, 10); got != 0 {
		t.Errorf("into a block whose copy is kept already: room %d blocks, want 0", got/blockSize)
	}
}

// A hash cached for a file's partial last block is of the block at that
// length. Once the file grows past it, the block is longer (zeros fill it)
// and the hash no longer describes it: a commit reusing it would name a
// block Modal has at the old length, and the file would read short.
func TestGrowthDropsBlockHash(t *testing.T) {
	st := newFileState(testFS(t, t.TempDir()), "a", "1", false, modalapi.Entry{})
	if _, err := st.writeAt([]byte("A"), 0); err != nil {
		t.Fatal(err)
	}
	st.hashes[0] = []byte("the hash of block 0 as a 1-byte block") // as a commit or an upload ahead leaves it
	if _, err := st.writeAt([]byte("Z"), blockSize+5); err != nil {
		t.Fatal(err)
	}
	if st.hashes[0] != nil {
		t.Fatal("the hash of a block that grew was kept")
	}
}

// A commit hashes the snapshot's blocks at the snapshot's length. If the
// file grew meanwhile, the live last block is longer than the one just
// hashed, so that hash isn't kept for it.
func TestSnapshotHashNotKeptForGrownBlock(t *testing.T) {
	fs := testFS(t, t.TempDir())
	fs.c = &volume.Volume{} // hashes plaintext; nothing is committed
	st := newFileState(fs, "a", "1", false, modalapi.Entry{})
	if _, err := st.writeAt([]byte("A"), 0); err != nil {
		t.Fatal(err)
	}
	sn := &snapshot{gen: st.gen, size: st.size, dirty: map[int64]bool{0: true}, saved: map[int64]bool{}, floor: st.size}
	st.snap = sn
	if _, err := st.writeAt([]byte("Z"), blockSize+5); err != nil { // block 0 is a whole block now
		t.Fatal(err)
	}
	hashes, err := st.snapshotHashes(sn, modalapi.Entry{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := sha256.Sum256([]byte("A")); !bytes.Equal(hashes[0], want[:]) {
		t.Fatalf("the snapshot's block 0 hashed as %x, want the hash of %q", hashes[0], "A")
	}
	if st.hashes[0] != nil {
		t.Fatal("the hash of the snapshot's 1-byte block was kept for the live block, which is a whole block")
	}
}

// Changes a volume that isn't mounted left on disk count toward the limit.
// When they fill it, no upload can free room: writes are refused at once,
// saying why, rather than wait for good.
func TestLeftChangesFillTheLimit(t *testing.T) {
	const limit = 64 * blockSize
	c, err := newBlockCache(t.TempDir(), limit)
	if err != nil {
		t.Fatal(err)
	}
	c.addPending(limit - readRoom(limit))
	c.leave("vo-1", limit-readRoom(limit))
	if ok, _, stuck, left := c.room(blockSize); ok || !stuck || !left {
		t.Fatalf("full of what an unmounted volume left: ok=%v stuck=%v left=%v, want refused, for that reason", ok, stuck, left)
	}
}

// A truncate that cuts through a block zeroes its tail in place: the next
// sync must make that durable, or a power loss could bring the cut bytes
// back.
func TestTruncateZeroingIsSynced(t *testing.T) {
	st := newFileState(testFS(t, t.TempDir()), "a", "1", false, modalapi.Entry{})
	if _, err := st.writeAt(bytes.Repeat([]byte{7}, 1000), 0); err != nil {
		t.Fatal(err)
	}
	if err := st.sync(); err != nil {
		t.Fatal(err)
	}
	if err := st.truncate(10); err != nil {
		t.Fatal(err)
	}
	if !st.unsynced[0] {
		t.Fatal("the block truncate zeroed isn't synced by the next sync")
	}
}

// A sync that fails leaves what it couldn't sync for the next one, which
// mustn't report success without it.
func TestFailedSyncIsRetried(t *testing.T) {
	st := newFileState(testFS(t, t.TempDir()), "a", "1", false, modalapi.Entry{})
	if _, err := st.writeAt([]byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(st.blockFile(0), 0); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := st.sync(); err == nil {
			t.Fatalf("sync %d succeeded with its block unreadable", i+1)
		}
	}
	if err := os.Chmod(st.blockFile(0), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.sync(); err != nil {
		t.Fatal(err)
	}
}
