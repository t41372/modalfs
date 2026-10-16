package nfsfront

// Failure cases: LINK is decoded as SYMLINK, unsupported errors use the
// wrong union body, or FSINFO advertises operations modalfs cannot commit.
// Raw RPCs complement mounted tests: a kernel may reject from FSINFO alone.

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/modalfake"
	"github.com/t41372/modalfs/internal/vfs"
	nfs "github.com/willscott/go-nfs"
	"github.com/willscott/go-nfs-client/nfs/rpc"
)

func TestMetadataRPC(t *testing.T) {
	fake, err := modalfake.Start(t.TempDir(), modalfake.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer fake.Close()
	c, err := modalapi.New(modalapi.Config{ServerURL: fake.URL, TokenID: "ak-fake", TokenSecret: "as-fake"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	vol, err := c.LookupVolume(context.Background(), "metadata", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := fake.Seed(vol.ID, modalfake.SeedEntry{Path: "link", Target: "target", Mode: os.ModeSymlink | 0o777}, modalfake.SeedEntry{Path: "target", Data: []byte("safe")}); err != nil {
		t.Fatal(err)
	}
	v, err := vfs.New(c, vol, vfs.Options{CacheDir: t.TempDir(), WriteBack: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	b := &billyFS{fs: v}
	h := newHandler(b)
	root := h.ToHandle(b, nil)
	link := h.ToHandle(b, []string{"link"})
	target := h.ToHandle(b, []string{"target"})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go nfs.Serve(ln, h)
	client, err := rpc.DialTCP("tcp", ln.Addr().String(), false)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	header := func(proc uint32) rpc.Header {
		return rpc.Header{Rpcvers: 2, Prog: 100003, Vers: 3, Proc: proc, Cred: unixCred("metadata", uint32(os.Getuid()), uint32(os.Getgid()))}
	}
	type dirarg struct {
		Handle []byte
		Name   string
	}
	tests := []struct {
		name    string
		request any
		length  int
	}{
		{"LINK", struct {
			rpc.Header
			File []byte
			To   dirarg
		}{header(15), target, dirarg{root, "new-hard"}}, 16},
		{"SYMLINK", struct {
			rpc.Header
			Where  dirarg
			Attrs  [6]uint32
			Target string
		}{header(10), dirarg{root, "new-link"}, [6]uint32{}, "target"}, 12},
		{"READLINK", struct {
			rpc.Header
			Handle []byte
		}{header(5), link}, 8},
		{"REMOVE", struct {
			rpc.Header
			Where dirarg
		}{header(12), dirarg{root, "link"}}, 12},
		{"RENAME", struct {
			rpc.Header
			From, To dirarg
		}{header(14), dirarg{root, "link"}, dirarg{root, "moved"}}, 20},
		{"READ", struct {
			rpc.Header
			Handle []byte
			Offset uint64
			Count  uint32
		}{header(6), link, 0, 1}, 8},
		{"WRITE", struct {
			rpc.Header
			Handle        []byte
			Offset        uint64
			Count, Stable uint32
			Data          []byte
		}{header(7), link, 0, 1, 0, []byte("x")}, 12},
		{"SETATTR size", struct {
			rpc.Header
			Handle []byte
			Attrs  [9]uint32
		}{header(2), link, [9]uint32{0, 0, 0, 1, 0, 0, 0, 0, 0}}, 12},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := client.Call(tc.request)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			if len(data) != tc.length || binary.BigEndian.Uint32(data) != 10004 {
				t.Fatalf("reply=%x length=%d; want NOTSUPP and %d bytes", data, len(data), tc.length)
			}
		})
	}
	r, err := client.Call(struct {
		rpc.Header
		Handle []byte
	}{header(19), root})
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	if err != nil || len(data) < 4 {
		t.Fatal(err)
	}
	properties := binary.BigEndian.Uint32(data[len(data)-4:])
	if properties&3 != 0 || properties&16 == 0 {
		t.Fatalf("FSINFO properties=%x; links off, SETTIME unchanged", properties)
	}
}
