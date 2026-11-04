package nfsfront

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/modalfake"
	"github.com/t41372/modalfs/internal/vfs"
	uninorm "golang.org/x/text/unicode/norm"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newNamespaceFS(t *testing.T, ttl time.Duration) (*billyFS, *modalfake.Server, *modalapi.Client, string) {
	t.Helper()
	root := t.TempDir()
	fake, err := modalfake.Start(filepath.Join(root, "fake"), modalfake.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fake.Close)
	c, err := modalapi.New(modalapi.Config{ServerURL: fake.URL, TokenID: "ak-fake", TokenSecret: "as-fake"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	vol, err := c.LookupVolume(context.Background(), "review", true)
	if err != nil {
		t.Fatal(err)
	}
	f, err := vfs.New(c, vol, vfs.Options{CacheDir: filepath.Join(root, "cache"), WriteBack: time.Hour, DirCacheTTL: ttl, NameKey: func(s string) (string, error) { return uninorm.NFD.String(s), nil }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return &billyFS{fs: f}, fake, c, vol.ID
}

func seedNamespaceFile(t *testing.T, fake *modalfake.Server, id, name, data string) {
	t.Helper()
	if err := fake.Seed(id, modalfake.SeedEntry{Path: name, Data: []byte(data)}); err != nil {
		t.Fatal(err)
	}
}

func TestStaleAliasCannotTruncateDifferentFile(t *testing.T) {
	const nfc, nfd = "café", "cafe\u0301"
	b, fake, c, id := newNamespaceFS(t, time.Hour)
	seedNamespaceFile(t, fake, id, nfc, "original NFC")
	if _, _, err := b.LookupNamed("", nfd); err != nil {
		t.Fatal(err)
	}
	seedNamespaceFile(t, fake, id, nfd, "new distinct NFD")
	raw, _, err := b.LookupNamed("", nfd)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.OpenFile(raw, os.O_RDWR|os.O_TRUNC, 0644); !errors.Is(err, syscall.ENOTSUP) {
		t.Fatalf("ambiguous truncation: %v", err)
	}
	for name, want := range map[string]string{nfc: "original NFC", nfd: "new distinct NFD"} {
		data, err := c.ReadFile(context.Background(), id, name, 1024)
		if err != nil || string(data) != want {
			t.Fatalf("changed %q: %q %v", name, data, err)
		}
	}
}

func TestCachedMissingNameCannotCreateCanonicalCollision(t *testing.T) {
	const nfc, nfd = "café", "cafe\u0301"
	b, fake, c, id := newNamespaceFS(t, time.Hour)
	if _, err := b.ReadDir(""); err != nil {
		t.Fatal(err)
	}
	seedNamespaceFile(t, fake, id, nfc, "existing NFC")
	if _, _, err := b.CreateNamed("", nfd, true, nil, nil); !errors.Is(err, os.ErrExist) {
		t.Fatalf("exclusive create: %v", err)
	}
	entries, err := c.List(context.Background(), id, "", false)
	if err != nil || len(entries) != 1 || entries[0].Path != nfc {
		t.Fatalf("canonical collision created: %+v %v", entries, err)
	}
}

type denyNamespaceListing struct {
	pb.ModalClientClient
	calls *atomic.Int64
}

func (d denyNamespaceListing) VolumeListFiles2(context.Context, *pb.VolumeListFiles2Request, ...grpc.CallOption) (grpc.ServerStreamingClient[pb.VolumeListFiles2Response], error) {
	d.calls.Add(1)
	return nil, status.Error(codes.PermissionDenied, "simulated revoked Modal token")
}

func TestLocalCommitDoesNotNeedModal(t *testing.T) {
	b, _, c, _ := newNamespaceFS(t, time.Nanosecond)
	if err := b.fs.Create("pending", true); err != nil {
		t.Fatal(err)
	}
	if _, err := b.fs.WriteAt("pending", []byte("durable locally"), 0); err != nil {
		t.Fatal(err)
	}
	original := c.Stub
	calls := &atomic.Int64{}
	c.Stub = denyNamespaceListing{original, calls}
	defer func() { c.Stub = original }()
	if err := b.fs.Sync("pending"); err != nil {
		t.Fatalf("direct local fsync failed: %v", err)
	}
	if err := b.SyncPath("pending"); err != nil {
		t.Fatalf("NFS COMMIT depended on Modal: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("local COMMIT made %d metadata RPCs", calls.Load())
	}
}

func TestRawHandleCannotRetargetCanonicalSuccessor(t *testing.T) {
	const nfc, nfd = "café", "cafe\u0301"
	b, fake, c, id := newNamespaceFS(t, time.Nanosecond)
	seedNamespaceFile(t, fake, id, nfc, "original NFC")
	raw, _, err := b.LookupNamed("", nfd)
	if err != nil {
		t.Fatal(err)
	}
	h := newHandler(b)
	handle := h.ToHandle(b, []string{raw})
	if err := c.Remove(context.Background(), id, nfc, false); err != nil {
		t.Fatal(err)
	}
	seedNamespaceFile(t, fake, id, nfd, "different successor")
	fs, parts, err := h.FromHandle(handle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.OpenFile(fs.Join(parts...), os.O_RDWR|os.O_TRUNC, 0644); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old raw handle: %v", err)
	}
	data, err := c.ReadFile(context.Background(), id, nfd, 1024)
	if err != nil || string(data) != "different successor" {
		t.Fatalf("successor overwritten: %q %v", data, err)
	}
}
