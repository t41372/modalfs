package vfs

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"syscall"
	"testing"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"github.com/t41372/modalfs/internal/modalfake"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Size and second-resolution mtime can stay equal across our own commit.
// A read planned before it must still fill blocks that changed from dirty
// to clean, rather than return the untouched bytes of its destination buffer.
func TestReadDuringSameSecondCommit(t *testing.T) {
	testReadDuringCommitCompletion(t, false, false)
}

// A commit to a conflict copy detaches the state without replacing its
// base. A read must report that its planned local blocks are gone.
func TestReadDuringConflictCopyCommit(t *testing.T) {
	testReadDuringCommitCompletion(t, true, false)
}

func TestReadDuringOwnThenConflictCopyCommit(t *testing.T) {
	testReadDuringCommitCompletion(t, true, true)
}

func testReadDuringCommitCompletion(t *testing.T, conflict, priorCommit bool) {
	t.Helper()
	f := newFakeFS(t, Options{WriteBack: time.Hour, MaxPending: -1, ReadAhead: -1})
	base := randBytes(2 * blockSize)
	f.put(t, "f", base)
	a, err := f.Stat("f")
	if err != nil {
		t.Fatal(err)
	}
	// Learn both hashes without caching data, so the commit doesn't need a
	// download but the read still needs block 0 from Modal.
	for i := int64(0); i < 2; i++ {
		if _, _, err := f.remoteBlock("f", a.Size, a.Mtime.Unix(), i, false); err != nil {
			t.Fatal(err)
		}
	}
	want := bytes.Clone(base)
	copy(want[blockSize:], bytes.Repeat([]byte("X"), blockSize))
	if _, err := f.WriteAt("f", want[blockSize:], blockSize); err != nil {
		t.Fatal(err)
	}
	reading, release := make(chan struct{}), make(chan struct{})
	var readOnce, writeOnce sync.Once
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	f.fake.SetOptions(modalfake.Options{InterceptRPC: func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		resp, err := next(ctx, req)
		if err != nil {
			return resp, err
		}
		switch r := req.(type) {
		case *pb.VolumeGetFile2Request:
			if r.GetPath() == "f" {
				readOnce.Do(func() {
					close(reading)
					<-release
				})
			}
		case *pb.VolumePutFiles2Request:
			if len(resp.(*pb.VolumePutFiles2Response).GetMissingBlocks()) != 0 {
				break
			}
			if priorCommit && r.GetFiles()[0].GetPath() == "f" {
				// Keep this same state alive after the first own commit, with
				// a newer write that the next commit saves as a conflict copy.
				writeOnce.Do(func() { _, err = f.WriteAt("f", []byte("Z"), blockSize+10) })
			}
			if !conflict {
				// Force the legal same-second outcome without relying on how
				// quickly the test runs across a wall-clock second boundary.
				err = f.fake.Seed(f.vol.ID, modalfake.SeedEntry{Path: "f", Data: want, Mtime: a.Mtime.Unix()})
			}
		}
		return resp, err
	}})
	buf := make([]byte, len(want))
	done := make(chan error, 1)
	go func() {
		_, err := f.ReadAt("f", buf, 0)
		done <- err
	}()
	select {
	case <-reading:
	case <-time.After(10 * time.Second):
		t.Fatal("read didn't reach Modal")
	}
	if priorCommit {
		if err := f.Flush("f"); err != nil {
			t.Fatal(err)
		}
	}
	if conflict {
		if err := f.Truncate("f", blockSize+123); err != nil {
			t.Fatal(err)
		}
		theirs := bytes.Clone(base)
		theirs[0] ^= 0xff
		if err := f.fake.Seed(f.vol.ID, modalfake.SeedEntry{Path: "f", Data: theirs, Mtime: a.Mtime.Unix() - 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Flush("f"); err != nil {
		t.Fatal(err)
	}
	unblock()
	select {
	case err := <-done:
		if conflict {
			if !errors.Is(err, syscall.ESTALE) {
				t.Fatalf("read racing a conflict-copy commit: %v, want ESTALE", err)
			}
		} else if err != nil || !bytes.Equal(buf, want) {
			t.Fatalf("read racing a commit with equal size and mtime: %v; content matches: %v", err, bytes.Equal(buf, want))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("read didn't resume")
	}
}

// Only ESTALE waits for a commit, and an unchanged base must preserve that
// error. In particular, a foreign writer's same-size replacement cannot be
// silently learned as the base that our pending changes were made on.
func TestReadWithCommitHeld(t *testing.T) {
	for _, name := range []string{"unchanged", "size changed", "hash changed", "permission denied"} {
		t.Run(name, func(t *testing.T) {
			f := newFakeFS(t, Options{WriteBack: time.Hour, MaxPending: -1, ReadAhead: -1})
			base := randBytes(blockSize)
			f.put(t, "f", base)
			a, err := f.Stat("f")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := f.remoteBlock("f", a.Size, a.Mtime.Unix(), 0, false); err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteAt("f", []byte("ours"), blockSize); err != nil {
				t.Fatal(err)
			}
			stale := name == "size changed" || name == "hash changed"
			if stale {
				size := blockSize
				if name == "size changed" {
					size--
				}
				f.put(t, "f", randBytes(size))
			}
			checked := make(chan struct{})
			var once sync.Once
			f.fake.SetOptions(modalfake.Options{InterceptRPC: func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
				if _, ok := req.(*pb.VolumeGetFile2Request); ok {
					defer once.Do(func() { close(checked) })
					if name == "permission denied" {
						return nil, status.Error(codes.PermissionDenied, "blocks refused")
					}
				}
				return next(ctx, req)
			}})
			st := f.files["f"]
			st.commitMu.Lock()
			unblock := sync.OnceFunc(st.commitMu.Unlock)
			defer unblock()
			buf := make([]byte, 4096)
			done := make(chan error, 1)
			go func() {
				_, err := f.ReadAt("f", buf, 0)
				done <- err
			}()
			select {
			case <-checked:
			case <-time.After(10 * time.Second):
				t.Fatal("read didn't reach Modal")
			}
			if stale {
				select {
				case err := <-done:
					t.Fatalf("stale read didn't wait for the commit: %v", err)
				case <-time.After(100 * time.Millisecond):
				}
				unblock()
			}
			select {
			case err := <-done:
				switch {
				case stale && !errors.Is(err, syscall.ESTALE):
					t.Fatalf("foreign replacement: %v, want ESTALE", err)
				case name == "permission denied" && status.Code(err) != codes.PermissionDenied:
					t.Fatalf("refused read: %v, want PermissionDenied", err)
				case name == "unchanged" && (err != nil || !bytes.Equal(buf, base[:len(buf)])):
					t.Fatalf("unchanged read during upload: %v; content matches: %v", err, bytes.Equal(buf, base[:len(buf)]))
				}
			case <-time.After(10 * time.Second):
				t.Fatal("read blocked on a commit without ESTALE, or failed to resume")
			}
		})
	}
}
