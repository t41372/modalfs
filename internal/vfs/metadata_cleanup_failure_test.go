package vfs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type failMetadataRemoval struct {
	pb.ModalClientClient
	path   string
	denied atomic.Bool
}

func (s *failMetadataRemoval) VolumeRemoveFile2(ctx context.Context, r *pb.VolumeRemoveFile2Request, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	if r.GetPath() == s.path && s.denied.Load() {
		return nil, status.Error(codes.PermissionDenied, "injected metadata removal failure")
	}
	return s.ModalClientClient.VolumeRemoveFile2(ctx, r, opts...)
}

func TestMetadataRemovalFailureKeepsPendingWrites(t *testing.T) {
	f := newFakeFS(t, Options{ReadAhead: -1, WriteBack: time.Hour, MaxPending: -1})
	f.put(t, "target", []byte("old-data"))
	stub := &failMetadataRemoval{ModalClientClient: f.client.Stub, path: f.sidecarPath("target")}
	stub.denied.Store(true)
	f.client.Stub = stub
	if _, err := f.WriteExistingAt("target", []byte("new-data"), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync("target"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	journal := filepath.Join(f.files["target"].dir, "meta.json")
	f.mu.Unlock()
	if err := f.Flush("target"); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("cleanup error hidden: %v", err)
	}
	if got := remoteDigest(t, f, "target"); got != fmt.Sprintf("%x", hashOf("old-data")) {
		t.Fatalf("failed cleanup published data: %s", got)
	}
	if _, err := os.Stat(journal); err != nil {
		t.Fatalf("failed cleanup discarded journal: %v", err)
	}
	buf := make([]byte, 8)
	if _, err := f.ReadAt("target", buf, 0); err != nil || string(buf) != "new-data" {
		t.Fatalf("failed cleanup lost writes: %q %v", buf, err)
	}
	stub.denied.Store(false)
	if err := f.Flush("target"); err != nil {
		t.Fatalf("retry after cleanup repair failed: %v", err)
	}
	if got := remoteDigest(t, f, "target"); got != fmt.Sprintf("%x", hashOf("new-data")) {
		t.Fatalf("retry published wrong bytes: %s", got)
	}
}
