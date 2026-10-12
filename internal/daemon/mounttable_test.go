package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMountTableHasRoot(t *testing.T) {
	table, err := mountedPaths()
	if err != nil {
		t.Fatal(err)
	}
	if !table["/"] {
		t.Fatalf("/ is not in the mount table: %v", table)
	}
	if m, ok := isMountpoint("/"); !m || !ok {
		t.Fatalf("isMountpoint(/) = %v, %v", m, ok)
	}
	dir := t.TempDir()
	if m, ok := isMountpoint(dir); m || !ok {
		t.Fatalf("isMountpoint(%s) = %v, %v; want false, true", dir, m, ok)
	}
}

func TestTablePathResolvesParent(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got := tablePath(filepath.Join(link, "mnt"))
	want, _ := filepath.EvalSymlinks(real)
	if got != filepath.Join(want, "mnt") {
		t.Fatalf("tablePath = %s, want %s/mnt", got, want)
	}
}

func TestUnescapeMountinfo(t *testing.T) {
	for in, want := range map[string]string{
		`/plain`:                "/plain",
		`/My\040Volumes/a\011b`: "/My Volumes/a\tb",
		`/back\134slash`:        `/back\slash`,
		`/not\08`:               `/not\08`,
		`/end\04`:               `/end\04`,
	} {
		if got := unescapeMountinfo(in); got != want {
			t.Errorf("unescapeMountinfo(%q) = %q, want %q", in, got, want)
		}
	}
}
