package vfs

import (
	"errors"
	"syscall"
	"testing"
	"time"

	"golang.org/x/text/unicode/norm"
)

func TestResolveNormalizedPath(t *testing.T) {
	f := newFakeFS(t, Options{WriteBack: time.Hour})
	f.put(t, "café/â\u0084¢.rar", []byte("remote"))
	for _, spelling := range []string{"café/â\u0084¢.rar", norm.NFD.String("café/â\u0084¢.rar")} {
		if got, err := f.ResolveNormalizedPath(spelling); err != nil || got != "café/â\u0084¢.rar" {
			t.Fatalf("resolve %q: %q, %v", spelling, got, err)
		}
	}
	if got, err := f.ResolveNormalizedPath("cafe\u0301/new/sub/file"); err != nil || got != "café/new/sub/file" {
		t.Fatalf("new path: %q, %v", got, err)
	}
	if err := f.Create("café/résumé", true); err != nil {
		t.Fatal(err)
	}
	if got, err := f.ResolveNormalizedPath(norm.NFD.String("café/résumé")); err != nil || got != "café/résumé" {
		t.Fatalf("pending local file: %q, %v", got, err)
	}
}

func TestResolveNormalizedPathRefusesAmbiguity(t *testing.T) {
	for _, pair := range [][2]string{{"café", "cafe\u0301"}, {"Å", "A\u030a"}, {"ά", "ά"}} {
		t.Run(pair[0], func(t *testing.T) {
			f := newFakeFS(t, Options{})
			f.put(t, pair[0], []byte("first"))
			f.put(t, pair[1], []byte("second"))
			for _, spelling := range []string{pair[0], pair[1], norm.NFD.String(pair[0])} {
				if _, err := f.ResolveNormalizedPath(spelling); !errors.Is(err, syscall.ENOTSUP) {
					t.Fatalf("ambiguous name %q: %v", spelling, err)
				}
			}
		})
	}
}

func TestMacOSUnicodeExclusions(t *testing.T) {
	f := newFakeFS(t, Options{})
	for _, name := range []string{"K", "K", "Å", "Å", "\uf900", "\U0002f800"} {
		f.put(t, name, []byte(name))
	}
	for _, name := range []string{"K", "K", "Å", "Å", "\uf900", "\U0002f800"} {
		if got, err := f.ResolveNormalizedPath(DecomposeMacOSPath(name)); err != nil || got != name {
			t.Fatalf("macOS must distinguish %q: %q, %v", name, got, err)
		}
	}
	if got := DecomposeMacOSPath("café/KÅ\uf900\U0002f800/가"); got != "cafe\u0301/KÅ\uf900\U0002f800/\u1100\u1161" {
		t.Fatalf("macOS decomposition: %q", got)
	}
}
