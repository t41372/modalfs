package vfs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A dirty block file that isn't a full block (left by a failed first write)
// must not be recovered as data, and temp files are ignored.
func TestRecoverSkipsShortBlock(t *testing.T) {
	root := filepath.Join(t.TempDir(), "dirty")
	d := filepath.Join(root, "1-1")
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(journal{Path: "a", BaseExists: true, BaseSize: 2 * blockSize, TruncFloor: 2 * blockSize, Size: 2 * blockSize})
	write := func(name string, size int64) {
		if err := os.WriteFile(filepath.Join(d, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(filepath.Join(d, name), size); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(d, journalName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	write("0", 0)
	write("1", blockSize)
	write("1.tmp", 100)
	fs := testFS(t, t.TempDir())
	fs.recover(root)
	st := fs.files["a"]
	if st == nil {
		t.Fatal("journal not recovered")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.timer != nil {
		st.timer.Stop() // there is no client to commit to
	}
	if st.dirty[0] || !st.dirty[1] || len(st.dirty) != 1 {
		t.Fatalf("dirty blocks = %v, want only block 1", st.dirty)
	}
}
