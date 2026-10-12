//go:build integration

// Run: MODALFS_TEST_VOLUME=some-scratch-volume go test -tags integration ./internal/nfsfront
package nfsfront

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/vfs"
	nfsc "github.com/willscott/go-nfs-client/nfs"
	"github.com/willscott/go-nfs-client/nfs/rpc"
)

func TestNFSEndToEnd(t *testing.T) {
	volName := os.Getenv("MODALFS_TEST_VOLUME")
	if volName == "" {
		t.Skip("MODALFS_TEST_VOLUME not set")
	}
	cfg, err := modalapi.LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := modalapi.New(cfg)
	vol, err := c.LookupVolume(context.Background(), volName, true)
	if err != nil {
		t.Fatal(err)
	}
	newFS := func() *vfs.FS {
		v, err := vfs.New(c, vol, vfs.Options{CacheDir: t.TempDir(), CacheMaxBytes: 1 << 30, WriteBack: 500 * time.Millisecond, SidecarMin: 1 << 20})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	v := newFS()
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	go Serve(l, v, false)
	rc, err := rpc.DialTCP("tcp", l.Addr().String(), false)
	if err != nil {
		t.Fatal(err)
	}
	var m nfsc.Mount
	m.Client = rc
	tg, err := m.Mount("/", rpc.AuthNull)
	if err != nil {
		t.Fatal(err)
	}
	_ = tg.RemoveAll("/nfstest")
	if _, err := tg.Mkdir("/nfstest", 0o755); err != nil {
		t.Fatal("mkdir:", err)
	}
	if _, err := tg.Create("/nfstest/a.txt", 0o644); err != nil {
		t.Fatal("create:", err)
	}
	f, err := tg.OpenFile("/nfstest/a.txt", 0o644)
	if err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("0123456789abcdef"), (10<<20)/16) // 10 MiB = 2 blocks
	if _, err := f.Write(big); err != nil {
		t.Fatal("write:", err)
	}
	f.Close()
	if err := v.FlushAll(); err != nil {
		t.Fatal("flush:", err)
	}
	// partial overwrite inside block 1 through NFS WRITE at an offset
	f, _ = tg.OpenFile("/nfstest/a.txt", 0o644)
	f.Seek(9<<20, io.SeekStart)
	f.Write([]byte("PATCHED"))
	f.Close()
	copy(big[9<<20:], "PATCHED")
	if err := v.FlushAll(); err != nil {
		t.Fatal("flush2:", err)
	}
	if err := tg.Rename("/nfstest/a.txt", "/nfstest/b.txt"); err != nil {
		t.Fatal("rename:", err)
	}
	ents, err := tg.ReadDirPlus("/nfstest")
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range ents {
		names = append(names, e.Name())
	}
	t.Log("dir:", names)

	// verify from a completely fresh client (empty cache)
	v2 := newFS()
	got := make([]byte, len(big))
	n, err := v2.ReadAt("nfstest/b.txt", got, 0)
	if err != nil || n != len(big) || !bytes.Equal(got, big) {
		t.Fatalf("fresh read mismatch n=%d err=%v", n, err)
	}
	if _, err := v2.Stat("nfstest/a.txt"); !os.IsNotExist(err) {
		t.Fatalf("old name still exists: %v", err)
	}
	if err := tg.Remove("/nfstest/b.txt"); err != nil {
		t.Fatal("remove:", err)
	}
	if err := tg.RmDir("/nfstest"); err != nil {
		t.Fatal("rmdir:", err)
	}
}
