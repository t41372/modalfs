package vfs

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type failRenameSourceRemoval struct{ pb.ModalClientClient }

func (f failRenameSourceRemoval) VolumeRemoveFile2(ctx context.Context, req *pb.VolumeRemoveFile2Request, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	if req.GetPath() == "source" {
		return nil, status.Error(codes.PermissionDenied, "injected source removal failure")
	}
	return f.ModalClientClient.VolumeRemoveFile2(ctx, req, opts...)
}

func TestRenameErrorReportsPublishedDestinationAndDropsOldCache(t *testing.T) {
	f := newFakeFS(t, Options{WriteBack: time.Hour})
	f.put(t, "source", []byte("source"))
	f.put(t, "target", []byte("target"))
	buf := make([]byte, 6)
	if _, err := f.ReadAt("target", buf, 0); err != nil || string(buf) != "target" {
		t.Fatalf("preload target: %q %v", buf, err)
	}
	original := f.client.Stub
	f.client.Stub = failRenameSourceRemoval{original}
	defer func() { f.client.Stub = original }()
	err := f.Rename("source", "target")
	if err == nil || !RenameMayHaveChangedDestination(err) {
		t.Fatalf("partial rename not marked: %v", err)
	}
	if code := status.Code(err); code != codes.PermissionDenied {
		t.Fatalf("wrapped status lost: %v", code)
	}
	if _, err := f.ReadAt("target", buf, 0); err != nil || string(buf) != "source" {
		t.Fatalf("published destination cache stale: %q %v", buf, err)
	}
	if got, err := f.client.ReadFile(context.Background(), f.vol.ID, "source", 1024); err != nil || string(got) != "source" {
		t.Fatalf("source lost: %q %v", got, err)
	}
}

func TestRenamePreconditionDoesNotInvalidateDestination(t *testing.T) {
	f := newFakeFS(t, Options{})
	f.put(t, "source/keep", []byte("source"))
	f.put(t, "target/keep", []byte("target"))
	err := f.Rename("source", "target")
	if !errors.Is(err, syscall.ENOTEMPTY) {
		t.Fatalf("unexpected failure: %v", err)
	}
	if RenameMayHaveChangedDestination(err) {
		t.Fatal("precondition failure marked as destination mutation")
	}
}
