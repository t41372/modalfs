package vfs

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestNewDestinationsFitClientNamespace(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		t.Run(fmt.Sprintf("encrypted=%v", encrypted), func(t *testing.T) {
			opt := Options{NameKey: testNameKey, WriteBack: time.Hour}
			var f *fakeFS
			count := 80
			if encrypted {
				f, count = newEncryptedSidecarFS(t, opt), 46
			} else {
				f = newFakeFS(t, opt)
			}
			long := strings.Repeat("가", count)
			if _, err := f.c.Path(long); err != nil {
				t.Fatalf("stored name must fit the remote encoding: %v", err)
			}
			if err := f.c.WriteFile(f.ctx, "source", []byte("source data")); err != nil {
				t.Fatal(err)
			}
			ns := f.Names().WithClientNameLimit(255)
			guarded := false
			guard := func(string) func(error) { guarded = true; return nil }
			if _, _, err := ns.CreateGuarded("", long, true, nil, func(p string, _ bool) func(error) { return guard(p) }); !errors.Is(err, syscall.ENAMETOOLONG) {
				t.Errorf("create: %v", err)
			}
			if _, _, err := ns.MkdirGuarded("", long, guard); !errors.Is(err, syscall.ENAMETOOLONG) {
				t.Errorf("mkdir: %v", err)
			}
			if _, _, err := ns.RenameGuarded("", "source", "", long, func(p, _ string) func(error) { return guard(p) }); !errors.Is(err, syscall.ENAMETOOLONG) {
				t.Errorf("rename: %v", err)
			}
			f.mu.Lock()
			pending := len(f.files)
			f.mu.Unlock()
			if guarded || pending != 0 {
				t.Fatal("rejected destination reached an identity guard or reserved a journal state")
			}
			if _, err := f.remoteStat(long, true); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected destination was published: %v", err)
			}
			if got, err := f.c.ReadFile(f.ctx, "source", 100); err != nil || string(got) != "source data" {
				t.Fatalf("rejected rename changed source: %q %v", got, err)
			}
			// A remotely created long name can still be opened by raw spelling
			// and recovered through a rename to an addressable destination.
			if err := f.c.WriteFile(f.ctx, long, []byte("remote data")); err != nil {
				t.Fatal(err)
			}
			if _, _, err := ns.Create("", long, false, nil); err != nil {
				t.Fatalf("existing raw spelling became inaccessible: %v", err)
			}
			if raw, _, err := ns.Lookup("", long); err != nil || raw != long {
				t.Fatalf("raw recovery lookup: %q %v", raw, err)
			}
			if _, _, err := ns.Rename("", long, "", "recovered"); err != nil {
				t.Fatalf("long-to-short recovery: %v", err)
			}
			if got, err := f.c.ReadFile(f.ctx, "recovered", 100); err != nil || string(got) != "remote data" {
				t.Fatalf("recovery changed content: %q %v", got, err)
			}
		})
	}
}

func TestClientNameLimitViewsKeepListingAndCreationConsistent(t *testing.T) {
	f := newFakeFS(t, Options{NameKey: testNameKey, WriteBack: time.Hour})
	f.put(t, "a", []byte("short"))
	f.put(t, "long", []byte("longer"))
	for _, limit := range []int{0, 1, 3, 255} {
		ns := f.Names().WithClientNameLimit(limit)
		entries, err := ns.ReadDir("")
		if err != nil {
			t.Fatal(err)
		}
		want := 2
		if limit > 0 && limit < len("long") {
			want = 1
		}
		if len(entries) != want {
			t.Fatalf("limit %d returned %d entries; want %d", limit, len(entries), want)
		}
		_, _, err = ns.Create("", "new", true, nil)
		if limit > 0 && limit < len("new") {
			if !errors.Is(err, syscall.ENAMETOOLONG) {
				t.Fatalf("limit %d create: %v", limit, err)
			}
		} else if err != nil {
			t.Fatalf("limit %d rejected an addressable destination: %v", limit, err)
		}
		// Remove a successful reservation so each view starts identically.
		if _, err := f.Names().Remove("", "new"); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	if entries, err := f.Names().ReadDir(""); err != nil || len(entries) != 2 {
		t.Fatalf("derived view changed the original namespace: %+v %v", entries, err)
	}
}

func TestClientNameLimitKeepsLinuxNamesLiteral(t *testing.T) {
	f := newFakeFS(t, Options{WriteBack: time.Hour})
	name := strings.Repeat("가", 80)
	ns := f.Names().WithClientNameLimit(255)
	if _, _, err := ns.Create("", name, true, nil); err != nil {
		t.Fatalf("literal 240-byte name was rejected: %v", err)
	}
	if entries, err := ns.ReadDir(""); err != nil || len(entries) != 1 || entries[0].Name != name {
		t.Fatalf("literal client view: %+v %v", entries, err)
	}
}

func TestClientNameLimitChecksStoredAndConvertedNames(t *testing.T) {
	f := newFakeFS(t, Options{NameKey: testNameKey, WriteBack: time.Hour})
	// Native decomposition contracts Greek prosgegrammeni from three UTF-8
	// bytes to the two-byte iota. Both spellings must fit the same view.
	const raw = "\u1fbe"
	key, err := testNameKey(raw)
	if err != nil || len(raw) != 3 || len(key) != 2 {
		t.Fatalf("contracting-name fixture: raw=%x key=%x err=%v", raw, key, err)
	}
	ns := f.Names().WithClientNameLimit(2)
	if _, _, err := ns.Create("", raw, true, nil); !errors.Is(err, syscall.ENAMETOOLONG) {
		t.Fatalf("new stored name exceeds the view: %v", err)
	}
	if err := f.c.WriteFile(f.ctx, raw, []byte("existing")); err != nil {
		t.Fatal(err)
	}
	f.invalidate("")
	if _, _, err := ns.Rename("", key, "", "x"); err != nil {
		t.Fatalf("existing raw-name recovery: %v", err)
	}
}
