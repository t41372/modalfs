package vfs

import (
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/encryption"
	"github.com/t41372/modalfs/internal/modalfake"
	"github.com/t41372/modalfs/internal/volume"
)

func TestNamespaceSpecialEntryCapabilities(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		label := "plain"
		if encrypted {
			label = "encrypted"
		}
		for _, op := range []string{"unlink-socket", "rename-socket", "unlink-symlink", "rename-symlink"} {
			t.Run(label+"/"+op, func(t *testing.T) {
				opt := Options{NameKey: testNameKey, WriteBack: time.Hour}
				f := newFakeFS(t, opt)
				if encrypted {
					if err := f.FS.Close(); err != nil {
						t.Fatal(err)
					}
					opt = f.opt
					opt.Cipher = encryption.New(encryption.NewKey())
					remote := &volume.Volume{Client: f.client, ID: f.vol.ID, Cipher: opt.Cipher}
					if err := remote.WriteMarker(context.Background()); err != nil {
						t.Fatal(err)
					}
					mounted, err := New(f.client, f.vol, opt)
					if err != nil {
						t.Fatal(err)
					}
					f.FS = mounted
					t.Cleanup(func() { mounted.Close() })
				}
				source := "caf\u00e9.special"
				raw, err := f.c.Path(source)
				if err != nil {
					t.Fatal(err)
				}
				mode := os.ModeSocket | 0o755
				if strings.HasSuffix(op, "symlink") {
					mode = os.ModeSymlink | 0o777
				}
				if err := f.c.WriteFile(context.Background(), "target", []byte("keep target")); err != nil {
					t.Fatal(err)
				}
				if err := f.fake.Seed(f.vol.ID, modalfake.SeedEntry{Path: raw, Mode: mode, Target: "target", Mtime: 1000000000}); err != nil {
					t.Fatal(err)
				}
				ns := f.Names()
				if strings.HasPrefix(op, "unlink") {
					_, err = ns.Remove("", "cafe\u0301.special")
				} else {
					_, _, err = ns.Rename("", "cafe\u0301.special", "", "renamed")
				}
				if mode&os.ModeSymlink != 0 {
					if !errors.Is(err, syscall.ENOTSUP) {
						t.Fatalf("symlink operation must be refused: %v", err)
					}
					entries, serr := f.fake.Inspect(f.vol.ID)
					if serr != nil {
						t.Fatal(serr)
					}
					present := false
					for _, entry := range entries {
						if entry.Path == raw && entry.Target == "target" {
							present = true
						}
					}
					if !present {
						t.Fatal("refused operation removed or changed link")
					}
				} else {
					if err != nil {
						t.Fatalf("API supports this socket operation: %v", err)
					}
					if _, serr := f.Stat(source); !errors.Is(serr, os.ErrNotExist) {
						t.Fatalf("socket source remains: %v", serr)
					}
					if strings.HasPrefix(op, "rename") {
						attr, serr := f.Stat("renamed")
						if serr != nil || attr.Mode&os.ModeSocket == 0 {
							t.Fatalf("renamed socket lost its type: %+v, %v", attr, serr)
						}
					}
				}
				got, rerr := f.c.ReadFile(context.Background(), "target", 100)
				if rerr != nil || string(got) != "keep target" {
					t.Fatalf("target changed: %q, %v", got, rerr)
				}
			})
		}
	}
}
