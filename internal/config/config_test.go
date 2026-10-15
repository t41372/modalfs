package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A change that can't be saved isn't applied either: what modalfs goes by
// is what it will find after a restart.
func TestFailedUpdateIsNotApplied(t *testing.T) {
	t.Setenv("MODALFS_HOME", t.TempDir())
	s, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(Dir(), "config.json.tmp"), 0o700); err != nil {
		t.Fatal(err) // in the way of the save
	}
	if err := s.Update(func(f *File) error {
		f.Mounts = append(f.Mounts, Mount{ID: "mnt-1"})
		return nil
	}); err == nil {
		t.Fatal("the update was saved")
	}
	if _, ok := s.Mount("mnt-1"); ok {
		t.Fatal("a change that wasn't saved is applied")
	}
}
