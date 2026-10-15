package vfs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A dirty block file that isn't a full block can only be damage: the
// changes are set aside whole, and the user is told, rather than a file
// committed with a hole nobody wrote.
func TestRecoverSetsAsideDamagedBlock(t *testing.T) {
	cache := t.TempDir()
	root := filepath.Join(cache, "dirty")
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
	write("0", 100)
	write("1", blockSize)
	fs := testFS(t, cache)
	var told []string
	fs.opt.OnEvent = func(kind, path, detail string) { told = append(told, kind+" "+path+": "+detail) }
	fs.recover(root)
	if fs.files["a"] != nil {
		t.Fatal("changes with a damaged block were recovered, to be committed")
	}
	kept, _ := filepath.Glob(filepath.Join(cache, "dirty-orphan-*", "1-1", "1"))
	if len(kept) != 1 {
		t.Fatal("the changes weren't kept")
	}
	if len(told) != 1 || !strings.HasPrefix(told[0], "error a: ") || !strings.Contains(told[0], "block 0 is damaged") {
		t.Fatalf("told the user %q", told)
	}
}
