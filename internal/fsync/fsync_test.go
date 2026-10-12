package fsync

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFile(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "f"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("data"); err != nil {
		t.Fatal(err)
	}
	if err := File(f); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(filepath.Dir(f.Name()))
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := File(dir); err != nil {
		t.Fatalf("syncing a directory: %v", err)
	}
	f.Close()
	if err := File(f); err == nil {
		t.Fatal("syncing a closed file succeeded")
	}
}
