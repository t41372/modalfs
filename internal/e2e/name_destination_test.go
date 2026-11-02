//go:build e2e && darwin

package e2e

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/t41372/modalfs/internal/filename"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/modalfake"
)

func TestNewNamesFitNativeClientNamespace(t *testing.T) {
	existingLong := strings.Repeat("가", 80) + ".old"
	e := setup(t, modalfake.Options{}, nil, func(c *modalapi.Client, id string) {
		putFile(t, c, id, "source", []byte("keep source"))
		putFile(t, c, id, existingLong, []byte("existing remote data"))
	})
	key, err := filename.Key()
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"create", "mkdir", "rename"} {
		name := strings.Repeat("가", 80) + "." + op
		client, err := key(name)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: raw=%d bytes client=%d bytes", op, len(name), len(client))
		target := filepath.Join(e.mnt, name)
		switch op {
		case "create":
			err = os.WriteFile(target, []byte("must not become invisible"), 0o600)
		case "mkdir":
			err = os.Mkdir(target, 0o700)
		case "rename":
			err = os.Rename(filepath.Join(e.mnt, "source"), target)
		}
		if !errors.Is(err, syscall.ENAMETOOLONG) {
			t.Errorf("%s unaddressable destination: got %v; want ENAMETOOLONG", op, err)
		}
		entries, err := os.ReadDir(e.mnt)
		if err != nil {
			t.Fatal(err)
		}
		visible := false
		for _, entry := range entries {
			visible = visible || entry.Name() == name || entry.Name() == client
		}
		if _, err := os.Stat(target); err == nil && !visible {
			t.Errorf("%s created a raw-accessible destination omitted from directory listing", op)
		}
	}
	if got, err := os.ReadFile(filepath.Join(e.mnt, "source")); err != nil || string(got) != "keep source" {
		t.Errorf("rejected rename changed source: %q %v", got, err)
	}
	// Preserve POSIX access to already stored names so they can be recovered.
	if got, err := os.ReadFile(filepath.Join(e.mnt, existingLong)); err != nil || string(got) != "existing remote data" {
		t.Fatalf("existing raw spelling became inaccessible: %q %v", got, err)
	}
	if err := os.Rename(filepath.Join(e.mnt, existingLong), filepath.Join(e.mnt, "recovered")); err != nil {
		t.Fatalf("long-to-short recovery: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(e.mnt, "recovered")); err != nil || string(got) != "existing remote data" {
		t.Fatalf("recovery changed content: %q %v", got, err)
	}
}
