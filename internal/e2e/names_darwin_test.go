//go:build e2e && darwin

package e2e

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/fsync"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/modalfake"
)

// Finder decomposes Unicode filenames before looking up their attributes.
// Remote downloads may use composed names, including mojibake from a server's
// headers. They must remain readable and writable through macOS's spelling.
func TestMacOSNormalizedNames(t *testing.T) {
	const original = "café/download â\u0084¢.rar"
	symbols := map[string]string{"K": "one", "K": "second", "Å": "third file", "Å": "fourth filename"}
	e := setup(t, modalfake.Options{}, nil, func(c *modalapi.Client, id string) {
		putFile(t, c, id, original, []byte("remote download"))
		for name, data := range symbols {
			putFile(t, c, id, "symbols/"+name, []byte(data))
		}
	})
	local := filepath.Join(e.mnt, "cafe\u0301/download a\u0302\u0084¢.rar")
	if got, err := os.ReadFile(local); err != nil || string(got) != "remote download" {
		t.Fatalf("Finder's spelling: %q, %v", got, err)
	}
	// Exercise the same Foundation URL conversion that made Finder omit
	// these files even though a POSIX listing could see them.
	args := []string{"-e", `
import Foundation
let args = Array(CommandLine.arguments.dropFirst())
for i in stride(from: 0, to: args.count, by: 2) {
    let url = URL(fileURLWithPath: args[i])
    let values = try url.resourceValues(forKeys: [.fileSizeKey, .isHiddenKey])
    guard values.fileSize == Int(args[i + 1]), values.isHidden == false else {
        print("Unexpected file attributes:", url, values)
        exit(1)
    }
}
`, filepath.Join(e.mnt, original), "15"}
	for name, data := range symbols {
		p := filepath.Join(e.mnt, "symbols", name)
		if got, err := os.ReadFile(p); err != nil || string(got) != data {
			t.Fatalf("distinct macOS name %q: %q, %v", name, got, err)
		}
		args = append(args, p, strconv.Itoa(len(data)))
	}
	check := exec.Command("swift", args...)
	if out, err := check.CombinedOutput(); err != nil {
		t.Fatalf("Foundation couldn't resolve the downloaded file: %v\n%s", err, out)
	}
	if err := os.WriteFile(local, []byte("edited locally"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.waitRemote(original, []byte("edited locally"), maxHeld)
	entries, err := e.client.List(context.Background(), e.vol.ID, "café", false)
	if err != nil || len(entries) != 1 || entries[0].Path != original {
		t.Fatalf("editing created another spelling: %+v, %v", entries, err)
	}
	const clientRenamed = "cafe\u0301/renomme\u0301.rar"
	if err := os.Rename(local, filepath.Join(e.mnt, clientRenamed)); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(e.mnt, clientRenamed)); err != nil || string(got) != "edited locally" {
		t.Fatalf("renamed file: %q, %v", got, err)
	}
	if err := os.Remove(filepath.Join(e.mnt, clientRenamed)); err != nil {
		t.Fatal(err)
	}
	entries, err = e.client.List(context.Background(), e.vol.ID, "café", false)
	if err != nil && !modalapi.IsNotFound(err) || len(entries) != 0 {
		t.Fatalf("removing through macOS left a remote file: %+v, %v", entries, err)
	}
}

func TestMacOSPendingUnicodeRename(t *testing.T) {
	e := setup(t, modalfake.Options{}, []string{"-write-back", "1h", "-max-pending", "-1s"})
	source := filepath.Join(e.mnt, "café.tmp")
	file, err := os.OpenFile(source, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data := []byte("an editor's pending temporary file")
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := fsync.File(file); err != nil {
		t.Fatal(err)
	}
	if entries, err := e.client.List(context.Background(), e.vol.ID, "", false); err != nil || len(entries) != 0 {
		t.Fatalf("fixture already uploaded: %+v, %v", entries, err)
	}
	target := filepath.Join(e.mnt, "saved.txt")
	if err := os.Rename(filepath.Join(e.mnt, "cafe\u0301.tmp"), target); err != nil {
		t.Fatal(err)
	}
	if got, err := e.remote("saved.txt"); err != nil || string(got) != string(data) {
		t.Fatalf("pending rename did not commit the original bytes: %q, %v", got, err)
	}
	if _, err := file.WriteAt([]byte("A"), 0); err != nil {
		t.Fatalf("open handle did not follow its renamed file: %v", err)
	}
	if err := fsync.File(file); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(target); err != nil || len(got) != len(data) || got[0] != 'A' {
		t.Fatalf("renamed handle bytes: %q, %v", got, err)
	}
}

// Stream-safe NFD would insert a CGJ into plain, and a UTF-8 CF constructor
// could strip a leading BOM. Neither may redirect creation into protected.
func TestMacOSDistinctNativeNamesPreserveData(t *testing.T) {
	pairs := [][2]string{
		{"a" + strings.Repeat("\u0301", 31), "a" + strings.Repeat("\u0301", 30) + "\u034f\u0301"},
		{"\ufeffreport.txt", "report.txt"},
	}
	for i, pair := range pairs {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			created, protected := pair[0], pair[1]
			e := setup(t, modalfake.Options{}, nil, func(c *modalapi.Client, id string) {
				putFile(t, c, id, protected, []byte("the existing file must remain intact"))
			})
			file, err := os.OpenFile(filepath.Join(e.mnt, created), os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o644)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if _, err := file.Write([]byte("a distinct new file")); err != nil {
				t.Fatal(err)
			}
			if err := fsync.File(file); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			e.waitRemote(created, []byte("a distinct new file"), 30*time.Second)
			if got, err := e.remote(protected); err != nil || string(got) != "the existing file must remain intact" {
				t.Fatalf("distinct creation overwrote another name: %q, %v", got, err)
			}
		})
	}
}

func TestMacOSUnaddressableNameKeepsOtherEntries(t *testing.T) {
	raw := strings.Repeat("가", 80) + ".bin"
	client := strings.Repeat("\u1100\u1161", 80) + ".bin"
	e := setup(t, modalfake.Options{}, nil, func(c *modalapi.Client, id string) {
		putFile(t, c, id, raw, []byte("retained on the volume"))
		putFile(t, c, id, "notes.txt", []byte("visible"))
	})
	if _, err := os.Stat(filepath.Join(e.mnt, client)); !errors.Is(err, syscall.ENAMETOOLONG) {
		t.Fatalf("native client name limit: %v", err)
	}
	entries, err := os.ReadDir(e.mnt)
	if err != nil || len(entries) != 1 || entries[0].Name() != "notes.txt" {
		t.Fatalf("unaddressable name hid valid entries: %+v, %v", entries, err)
	}
	if got, err := e.remote(raw); err != nil || string(got) != "retained on the volume" {
		t.Fatalf("unaddressable entry was changed: %q, %v", got, err)
	}
	if !strings.Contains(e.logs.String(), "cannot be shown") {
		t.Fatal("unaddressable filename was omitted without a visible diagnostic")
	}
}

func TestMacOSNormalizedNamesRefuseCollision(t *testing.T) {
	const composed, decomposed = "café", "cafe\u0301"
	e := setup(t, modalfake.Options{}, nil, func(c *modalapi.Client, id string) {
		putFile(t, c, id, composed, []byte("first"))
		putFile(t, c, id, decomposed, []byte("second"))
	})
	if err := os.WriteFile(filepath.Join(e.mnt, decomposed), []byte("must not overwrite either"), 0o644); err == nil {
		t.Fatal("a write with an ambiguous Unicode name succeeded")
	}
	for name, want := range map[string]string{composed: "first", decomposed: "second"} {
		if got, err := e.remote(name); err != nil || string(got) != want {
			t.Fatalf("ambiguous write changed %q: %q, %v", name, got, err)
		}
	}
}
