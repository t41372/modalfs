package vfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"github.com/t41372/modalfs/internal/modalfake"
	bolt "go.etcd.io/bbolt"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestRemoveEmptyDirectoryForgetsDescendantHashes(t *testing.T) {
	for _, prefetch := range []bool{false, true} {
		name := "cached"
		if prefetch {
			name = "detached-prefetch"
		}
		t.Run(name, func(t *testing.T) {
			f := newFakeFS(t, Options{ReadAhead: 1})
			if err := f.Mkdir("dir"); err != nil {
				t.Fatal(err)
			}
			const mtime = 12345
			if err := f.fake.Seed(f.vol.ID, modalfake.SeedEntry{Path: "dir/child", Data: []byte("before"), Mtime: mtime}); err != nil {
				t.Fatal(err)
			}
			buf := make([]byte, 6)
			var release func()
			if prefetch {
				gate, unpause := pauseBlockDownload(t, f)
				release = unpause
				f.prefetch("dir/child", 6, mtime, 0)
				select {
				case <-gate.entered:
				case <-time.After(2 * time.Second):
					t.Fatal("old child prefetch did not pause")
				}
			} else if _, err := f.ReadAt("dir/child", buf, 0); err != nil || string(buf) != "before" {
				t.Fatalf("preload child: %q %v", buf, err)
			}
			// Another tool removed the child. The empty directory remains
			// because its keep marker exists, but old cache metadata survives.
			if err := f.c.Remove(f.ctx, "dir/child", false); err != nil {
				t.Fatal(err)
			}
			if err := f.Remove("dir"); err != nil {
				t.Fatal(err)
			}
			if err := f.fake.Seed(f.vol.ID, modalfake.SeedEntry{Path: "dir/child", Data: []byte("newnew"), Mtime: mtime}); err != nil {
				t.Fatal(err)
			}
			if release != nil {
				release()
				waitPrefetch(t, f)
			}
			if hash := f.mdb.Hash("dir/child", 6, mtime, 0); hash != nil {
				t.Fatal("directory deletion retained or revived descendant hashes")
			}
			if _, err := f.ReadAt("dir/child", buf, 0); err != nil || string(buf) != "newnew" {
				t.Fatalf("recreated child inherited old bytes: %q %v", buf, err)
			}
		})
	}
}

func TestRemoveCacheFailureDoesNotDiscardJournal(t *testing.T) {
	f := newFakeFS(t, Options{WriteBack: time.Hour, MaxPending: -1})
	f.put(t, "target", []byte("old-data"))
	if _, err := f.WriteExistingAt("target", []byte("new-data"), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync("target"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	journal := filepath.Join(f.files["target"].dir, "meta.json")
	f.mu.Unlock()
	manifestDiskReadOnly(t, f.mdb)
	attempted := false
	err := f.remove("target", RemoveAny, func() error { attempted = true; return nil })
	if !errors.Is(err, bolt.ErrDatabaseReadOnly) || attempted {
		t.Fatalf("cache failure must precede discard/mutation marker: attempted=%v err=%v", attempted, err)
	}
	if _, err := os.Stat(journal); err != nil {
		t.Fatalf("refused delete lost journal: %v", err)
	}
	if pending := f.PendingFiles(); len(pending) != 1 || pending[0].Path != "target" {
		t.Fatalf("refused delete lost pending local writes: %+v", pending)
	}
	buf := make([]byte, 8)
	if _, err := f.ReadAt("target", buf, 0); err != nil || string(buf) != "new-data" {
		t.Fatalf("refused delete lost local bytes: %q %v", buf, err)
	}
}

type holdMetadataRemoval struct {
	pb.ModalClientClient
	path             string
	entered, release chan struct{}
	once             sync.Once
}

func (g *holdMetadataRemoval) VolumeRemoveFile2(ctx context.Context, req *pb.VolumeRemoveFile2Request, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	if req.GetPath() == g.path {
		g.once.Do(func() { close(g.entered); <-g.release })
	}
	return g.ModalClientClient.VolumeRemoveFile2(ctx, req, opts...)
}

func TestRemoveFinishesSidecarCleanupBeforeSuccessorCanBeCreated(t *testing.T) {
	f := newFakeFS(t, Options{})
	f.put(t, "target", []byte("before"))
	a, err := f.Stat("target")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.writeSidecar("target", &manifest{Size: 6, Mtime: a.Mtime.Unix(), Hashes: f.c.HashBlocks([]byte("before"))}); err != nil {
		t.Fatal(err)
	}
	gate := &holdMetadataRemoval{ModalClientClient: f.client.Stub, path: f.sidecarPath("target"), entered: make(chan struct{}), release: make(chan struct{})}
	f.client.Stub = gate
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	t.Cleanup(release)
	done := make(chan error, 1)
	go func() { done <- f.Remove("target") }()
	select {
	case <-gate.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("sidecar cleanup did not start")
	}
	select {
	case err := <-done:
		t.Fatalf("remove returned while its old sidecar cleanup could delete a successor's sidecar: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f.put(t, "target", []byte("newnew"))
	f.invalidate("")
	a, err = f.Stat("target")
	if err != nil {
		t.Fatal(err)
	}
	m := &manifest{Size: 6, Mtime: a.Mtime.Unix(), Hashes: f.c.HashBlocks([]byte("newnew"))}
	if err := f.writeSidecar("target", m); err != nil {
		t.Fatal(err)
	}
	if got := f.loadSidecar("target", 6, m.Mtime); got == nil || string(got.Hashes[0]) != string(m.Hashes[0]) {
		t.Fatal("successor sidecar was removed by old cleanup")
	}
}

func TestCommitPurgesOldSidecarEvenWhenNewSidecarIsDisabled(t *testing.T) {
	f := newFakeFS(t, Options{ReadAhead: -1, WriteBack: time.Hour, MaxPending: -1, SidecarMin: -1})
	f.put(t, "target", []byte("old-data"))
	buf := make([]byte, 8)
	if _, err := f.ReadAt("target", buf, 0); err != nil {
		t.Fatal(err)
	}
	a, err := f.Stat("target")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.writeSidecar("target", &manifest{Size: 8, Mtime: a.Mtime.Unix(), Hashes: f.c.HashBlocks([]byte("old-data"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteExistingAt("target", []byte("new-data"), 0); err != nil {
		t.Fatal(err)
	}
	f.client.Stub = &cacheFailureCommit{ModalClientClient: f.client.Stub, after: func() {
		if err := f.fake.Seed(f.vol.ID, modalfake.SeedEntry{Path: "target", Data: []byte("new-data"), Mtime: a.Mtime.Unix()}); err != nil {
			t.Fatal(err)
		}
	}}
	if err := f.Flush("target"); err != nil {
		t.Fatal(err)
	}
	if err := f.mdb.Delete("target"); err != nil {
		t.Fatal(err)
	}
	if m := f.loadSidecar("target", 8, a.Mtime.Unix()); m != nil {
		t.Fatalf("old same-version sidecar remained authoritative: %x", m.Hashes)
	}
	if _, err := f.ReadAt("target", buf, 0); err != nil || string(buf) != "new-data" {
		t.Fatalf("replacement read stale sidecar bytes: %q %v", buf, err)
	}
}
