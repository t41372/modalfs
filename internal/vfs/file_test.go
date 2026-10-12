package vfs

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/t41372/modalfs/internal/modalapi"
)

func testFS(t *testing.T, cacheDir string) *FS {
	fs := &FS{opt: Options{CacheDir: cacheDir}, log: slog.New(slog.DiscardHandler), files: map[string]*fileState{}}
	fs.closed.Store(true) // no commit timers: there is no client
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
