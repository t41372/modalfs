package vfs

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"github.com/t41372/modalfs/internal/modalfake"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

type holdFirstBlockDownload struct {
	base             http.RoundTripper
	entered, release chan struct{}
	held             atomic.Bool
}

func (g *holdFirstBlockDownload) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := g.base.RoundTrip(req)
	if err == nil && req.Method == "GET" && strings.Contains(req.URL.Path, "/get/") && g.held.CompareAndSwap(false, true) {
		close(g.entered)
		<-g.release
	}
	return response, err
}
func pauseBlockDownload(t *testing.T, f *fakeFS) (*holdFirstBlockDownload, func()) {
	gate := &holdFirstBlockDownload{base: f.client.HTTP.Transport, entered: make(chan struct{}), release: make(chan struct{})}
	f.client.HTTP.Transport = gate
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	t.Cleanup(release)
	return gate, release
}
func waitPrefetch(t *testing.T, f *fakeFS) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		f.mu.Lock()
		n := len(f.inflight)
		f.mu.Unlock()
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("prefetch did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}

type copyAtKnownTuple struct {
	pb.ModalClientClient
	fake             *modalfake.Server
	volume           string
	mtime            int64
	data             []byte
	entered, release chan struct{}
}

func (g copyAtKnownTuple) VolumeCopyFiles2(ctx context.Context, req *pb.VolumeCopyFiles2Request, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	if g.entered != nil {
		close(g.entered)
		<-g.release
	}
	response, err := g.ModalClientClient.VolumeCopyFiles2(ctx, req, opts...)
	if err == nil {
		err = g.fake.Seed(g.volume, modalfake.SeedEntry{Path: req.GetDstPath(), Data: g.data, Mtime: g.mtime})
	}
	return response, err
}

func TestRenameDoesNotMoveUnverifiedSourceHashes(t *testing.T) {
	f := newFakeFS(t, Options{ReadAhead: -1})
	const mtime = 12345
	if err := f.fake.Seed(f.vol.ID, modalfake.SeedEntry{Path: "source", Data: []byte("before"), Mtime: mtime}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 6)
	if _, err := f.ReadAt("source", buf, 0); err != nil || string(buf) != "before" {
		t.Fatalf("preload: %q %v", buf, err)
	}
	if err := f.fake.Seed(f.vol.ID, modalfake.SeedEntry{Path: "source", Data: []byte("newnew"), Mtime: mtime}); err != nil {
		t.Fatal(err)
	}
	original := f.client.Stub
	f.client.Stub = copyAtKnownTuple{ModalClientClient: original, fake: f.fake, volume: f.vol.ID, mtime: mtime, data: []byte("newnew")}
	defer func() { f.client.Stub = original }()
	if err := f.Rename("source", "target"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ReadAt("target", buf, 0); err != nil || string(buf) != "newnew" {
		t.Fatalf("copied path inherited old source hashes: %q %v", buf, err)
	}
}

func TestDetachedPrefetchCannotPublishAcrossRename(t *testing.T) {
	for _, during := range []bool{false, true} {
		name := "before"
		if during {
			name = "during"
		}
		t.Run(name, func(t *testing.T) {
			f := newFakeFS(t, Options{ReadAhead: 1})
			const mtime = 12345
			if err := f.fake.Seed(f.vol.ID, modalfake.SeedEntry{Path: "source", Data: []byte("newnew"), Mtime: mtime}, modalfake.SeedEntry{Path: "target", Data: []byte("before"), Mtime: mtime}); err != nil {
				t.Fatal(err)
			}
			gate, release := pauseBlockDownload(t, f)
			original := f.client.Stub
			copyGate := copyAtKnownTuple{ModalClientClient: original, fake: f.fake, volume: f.vol.ID, mtime: mtime, data: []byte("newnew")}
			renameDone := make(chan error, 1)
			var copyRelease func()
			if during {
				copyGate.entered = make(chan struct{})
				copyGate.release = make(chan struct{})
				var once sync.Once
				copyRelease = func() { once.Do(func() { close(copyGate.release) }) }
				t.Cleanup(copyRelease)
			}
			f.client.Stub = copyGate
			defer func() { f.client.Stub = original }()
			if during {
				go func() { renameDone <- f.Rename("source", "target") }()
				<-copyGate.entered
			}
			f.prefetch("target", 6, mtime, 0)
			select {
			case <-gate.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("prefetch didn't reach old signed URL")
			}
			if during {
				copyRelease()
				if err := <-renameDone; err != nil {
					t.Fatal(err)
				}
			} else if err := f.Rename("source", "target"); err != nil {
				t.Fatal(err)
			}
			release()
			waitPrefetch(t, f)
			if hash := f.mdb.Hash("target", 6, mtime, 0); hash != nil {
				t.Fatal("detached old work republished a path association")
			}
			data := make([]byte, 6)
			if _, err := f.ReadAt("target", data, 0); err != nil || string(data) != "newnew" {
				t.Fatalf("renamed data poisoned by prefetch: %q %v", data, err)
			}
		})
	}
}

func TestSidecarLoadCannotPublishAcrossKnownMutation(t *testing.T) {
	f := newFakeFS(t, Options{})
	const mtime = 12345
	if err := f.fake.Seed(f.vol.ID, modalfake.SeedEntry{Path: "file", Data: []byte("before"), Mtime: mtime}); err != nil {
		t.Fatal(err)
	}
	if err := f.writeSidecar("file", &manifest{Size: 6, Mtime: mtime, Hashes: [][]byte{f.c.HashBlock([]byte("before"))}}); err != nil {
		t.Fatal(err)
	}
	gate, release := pauseBlockDownload(t, f)
	done := make(chan *manifest, 1)
	go func() { done <- f.loadSidecar("file", 6, mtime) }()
	select {
	case <-gate.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("sidecar download not paused")
	}
	f.forgetContent("file")
	if err := f.fake.Seed(f.vol.ID, modalfake.SeedEntry{Path: "file", Data: []byte("newnew"), Mtime: mtime}); err != nil {
		t.Fatal(err)
	}
	f.forgetContent("file")
	release()
	if result := <-done; result != nil {
		t.Fatal("old sidecar accepted after known mutation")
	}
	if hash := f.mdb.Hash("file", 6, mtime, 0); hash != nil {
		t.Fatal("old sidecar republished path hashes")
	}
}

func TestDetachedPrefetchCannotDowngradeCommittedManifest(t *testing.T) {
	f := newFakeFS(t, Options{ReadAhead: 1, WriteBack: time.Hour})
	f.put(t, "file", []byte("before"))
	old, err := f.client.List(context.Background(), f.vol.ID, "", false)
	if err != nil {
		t.Fatal(err)
	}
	gate, release := pauseBlockDownload(t, f)
	f.prefetch("file", 6, old[0].Mtime, 0)
	select {
	case <-gate.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("prefetch wasn't paused")
	}
	if _, err := f.WriteAt("file", []byte("newnew"), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Flush("file"); err != nil {
		t.Fatal(err)
	}
	current, err := f.client.List(context.Background(), f.vol.ID, "", false)
	if err != nil {
		t.Fatal(err)
	}
	release()
	waitPrefetch(t, f)
	want := f.c.HashBlock([]byte("newnew"))
	if got := f.mdb.Hash("file", 6, current[0].Mtime, 0); string(got) != string(want) {
		t.Fatalf("old prefetch displaced committed mapping: %x want %x", got, want)
	}
	data := make([]byte, 6)
	if _, err := f.ReadAt("file", data, 0); err != nil || string(data) != "newnew" {
		t.Fatalf("committed read: %q %v", data, err)
	}
}
