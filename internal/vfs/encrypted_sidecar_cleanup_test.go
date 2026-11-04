package vfs

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/encryption"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/volume"
)

func newEncryptedSidecarFS(t *testing.T, opt Options) *fakeFS {
	t.Helper()
	// Reuse the isolated fake fixture, then reopen it with an encryption
	// marker/key before writing any files (a remount, never a real volume).
	f := newFakeFS(t, opt)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	remote := &volume.Volume{Client: f.client, ID: f.vol.ID, Cipher: encryption.New(encryption.NewKey())}
	if err := remote.WriteMarker(context.Background()); err != nil {
		t.Fatal(err)
	}
	opt.CacheDir, opt.Logger, opt.Cipher = f.opt.CacheDir, f.opt.Logger, remote.Cipher
	fs, err := New(f.client, f.vol, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	return &fakeFS{FS: fs, fake: f.fake, client: f.client, vol: f.vol}
}

func corruptEncryptedSidecar(t *testing.T, f *fakeFS, p string) string {
	t.Helper()
	raw, err := f.c.Path(f.sidecarPath(p))
	if err != nil {
		t.Fatal(err)
	}
	// Exactly the actual E2E fixture's corruption: only four ciphertext
	// bytes remain, shorter than even an authenticated empty block.
	putRaw(t, f, raw, []byte("bad!"))
	return raw
}

func putRaw(t *testing.T, f *fakeFS, raw string, data []byte) {
	t.Helper()
	_, err := f.client.Put(f.ctx, f.vol.ID, []modalapi.PutFile{{Path: raw, Size: int64(len(data)), Hashes: modalapi.HashBlocks(data), Block: func(int) ([]byte, error) { return data, nil }}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEncryptedCorruptSidecarDoesNotBlockAuthenticatedCommit(t *testing.T) {
	reported := make(chan string, 4)
	f := newEncryptedSidecarFS(t, Options{ReadAhead: -1, SidecarMin: 1, WriteBack: time.Hour, MaxPending: -1, OnEvent: func(kind, p, detail string) {
		if kind == "unreadable" && strings.Contains(detail, "rebuild hashes") {
			select {
			case reported <- detail:
			default:
			}
		}
	}})
	p := strings.Repeat("x", 147)
	data := bytes.Repeat([]byte("a"), 2*int(f.chunkSize())+257)
	if err := f.c.WriteFile(f.ctx, p, data); err != nil {
		t.Fatal(err)
	}
	a, err := f.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.writeSidecar(p, &manifest{Size: a.Size, Mtime: a.Mtime.Unix(), Hashes: f.c.HashBlocks(data)}); err != nil {
		t.Fatal(err)
	}
	corruptEncryptedSidecar(t, f, p)
	if m := f.loadSidecar(p, a.Size, a.Mtime.Unix()); m != nil {
		t.Fatal("truncated encrypted metadata was trusted")
	}
	patch := []byte("edit")
	off := f.chunkSize() + 16
	if _, err := f.WriteExistingAt(p, patch, off); err != nil {
		t.Fatal(err)
	}
	copy(data[off:], patch)
	if err := f.Flush(p); err != nil {
		t.Fatalf("disposable encrypted sidecar blocked authenticated payload commit: %v", err)
	}
	actual, err := f.c.ReadFile(f.ctx, p, int64(len(data)))
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatalf("repaired commit changed authenticated bytes: size=%d err=%v", len(actual), err)
	}
	a, err = f.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.mdb.Delete(p); err != nil {
		t.Fatal(err)
	}
	m := f.loadSidecar(p, a.Size, a.Mtime.Unix())
	if m == nil {
		t.Fatal("encrypted sidecar was not rebuilt")
	}
	want := f.c.HashBlocks(data)
	for i, h := range want {
		if !bytes.Equal(m.Hashes[i], h) {
			t.Fatalf("rebuilt sidecar block %d is wrong", i)
		}
	}
	select {
	case <-reported:
	default:
		t.Fatal("damaged optional metadata did not report hash rebuilding")
	}
}

func TestEncryptedMetadataTreePurgeKeepsPayloadRefusalChecks(t *testing.T) {
	f := newEncryptedSidecarFS(t, Options{ReadAhead: -1})
	if err := f.c.WriteFile(f.ctx, "source/child", []byte("payload")); err != nil {
		t.Fatal(err)
	}
	a, err := f.Stat("source/child")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.writeSidecar("source/child", &manifest{Size: 7, Mtime: a.Mtime.Unix(), Hashes: f.c.HashBlocks([]byte("payload"))}); err != nil {
		t.Fatal(err)
	}
	metadata := corruptEncryptedSidecar(t, f, "source/child")
	badPayload, err := f.c.Path("unrelated/bad")
	if err != nil {
		t.Fatal(err)
	}
	putRaw(t, f, badPayload, []byte("bad!"))
	if err := f.Rename("source", "moved"); err != nil {
		t.Fatalf("damaged descendant metadata blocked directory move: %v", err)
	}
	if _, err := f.client.ReadFile(f.ctx, f.vol.ID, metadata, 1024); !modalapi.IsNotFound(err) {
		t.Fatalf("old damaged descendant sidecar survived: %v", err)
	}
	if actual, err := f.c.ReadFile(f.ctx, "moved/child", 1024); err != nil || string(actual) != "payload" {
		t.Fatalf("metadata cleanup affected payload: %q %v", actual, err)
	}
	if err := f.c.Remove(f.ctx, "unrelated", true); !errors.Is(err, volume.ErrUnreadable) {
		t.Fatalf("unreadable payload removal was not refused: %v", err)
	}
	if err := f.c.Copy(f.ctx, []string{"unrelated"}, "copy", true); !errors.Is(err, volume.ErrUnreadable) {
		t.Fatalf("unreadable payload copy was not refused: %v", err)
	}
	if actual, err := f.client.ReadFile(f.ctx, f.vol.ID, badPayload, 1024); err != nil || string(actual) != "bad!" {
		t.Fatalf("reserved metadata purge deleted unrelated payload: %q %v", actual, err)
	}
}
