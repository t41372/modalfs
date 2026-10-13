package vfs

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/modalapi"
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
// nothing to upload.
func TestRoomForPending(t *testing.T) {
	const limit = 64 * blockSize
	c, err := newBlockCache(t.TempDir(), limit)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _, _ := c.room(blockSize); !ok {
		t.Fatal("no room in an empty cache")
	}
	c.addPending(limit - readRoom(limit))
	ok, freed, stuck := c.room(blockSize)
	if ok || stuck {
		t.Fatalf("full: ok=%v stuck=%v, want a wait", ok, stuck)
	}
	c.addPending(-blockSize)
	select {
	case <-freed:
	default:
		t.Fatal("uploading a block didn't wake the waiting writes")
	}
	if ok, _, _ := c.room(blockSize); !ok {
		t.Fatal("no room after a block was uploaded")
	}
	c.addPending(-(limit - readRoom(limit) - blockSize))
	if ok, _, stuck := c.room(limit); ok || !stuck {
		t.Fatalf("a write bigger than the limit with nothing pending: ok=%v stuck=%v, want stuck", ok, stuck)
	}
}

// A closed mount stops counting its pending changes: they're the next
// mount's to count, and left counted they'd make other mounts wait forever.
func TestClosedMountStopsCounting(t *testing.T) {
	fs := testFS(t, t.TempDir())
	fs.closed.Store(true)
	st := newFileState(fs, "a", "1", false, modalapi.Entry{})
	if _, err := st.writeAt([]byte("x"), 0); err != nil {
		t.Fatal(err)
	}
	if got := fs.cache.Pending(); got != 0 {
		t.Fatalf("pending = %d on a closed mount, want 0", got)
	}
}
