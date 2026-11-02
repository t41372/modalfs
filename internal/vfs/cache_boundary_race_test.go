package vfs

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/t41372/modalfs/internal/modalfake"
)

func TestWorkStartedDuringRemoveCannotRepublishDescendantHashes(t *testing.T) {
	f := newFakeFS(t, Options{ReadAhead: -1})
	if err := f.Mkdir("dir"); err != nil {
		t.Fatal(err)
	}
	const mtime = 12345
	if err := f.fake.Seed(f.vol.ID, modalfake.SeedEntry{Path: "dir/child", Data: []byte("before"), Mtime: mtime}); err != nil {
		t.Fatal(err)
	}
	urls, _, err := f.c.BlockURLs(f.ctx, "dir/child", 0, 1)
	if err != nil || len(urls) != 1 {
		t.Fatalf("old child URL: %v %v", urls, err)
	}
	if err := f.c.Remove(f.ctx, "dir/child", false); err != nil {
		t.Fatal(err)
	}
	removeGate := &holdMetadataRemoval{ModalClientClient: f.client.Stub, path: "dir", entered: make(chan struct{}), release: make(chan struct{})}
	f.client.Stub = removeGate
	var once sync.Once
	releaseRemove := func() { once.Do(func() { close(removeGate.release) }) }
	t.Cleanup(releaseRemove)
	removeDone := make(chan error, 1)
	go func() { removeDone <- f.Remove("dir") }()
	select {
	case <-removeGate.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Remove RPC did not pause")
	}
	// Simulate an already obtained signed URL whose background job starts
	// during the Remove RPC, after the initial invalidation. Only the final
	// mutation fence can reject this job's path/hash publication.
	gate, releaseBlock := pauseBlockDownload(t, f)
	blockDone := make(chan error, 1)
	epoch := f.contentEpoch()
	go func() {
		_, _, err := f.remoteBlockAtEpoch("dir/child", 6, mtime, 0, true, urls[0], epoch)
		blockDone <- err
	}()
	select {
	case <-gate.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("old signed URL download did not pause")
	}
	releaseRemove()
	if err := <-removeDone; err != nil {
		t.Fatal(err)
	}
	if err := f.fake.Seed(f.vol.ID, modalfake.SeedEntry{Path: "dir/child", Data: []byte("newnew"), Mtime: mtime}); err != nil {
		t.Fatal(err)
	}
	releaseBlock()
	if err := <-blockDone; err != nil {
		t.Fatal(err)
	}
	if hash := f.mdb.Hash("dir/child", 6, mtime, 0); hash != nil {
		t.Fatal("work started during Remove republished old descendant mapping")
	}
	buf := make([]byte, 6)
	if _, err := f.ReadAt("dir/child", buf, 0); err != nil || string(buf) != "newnew" {
		t.Fatalf("recreated descendant read stale bytes: %q %v", buf, err)
	}
}

func TestRemoveWaitsForOldSidecarWriter(t *testing.T) {
	f := newFakeFS(t, Options{WriteBack: time.Hour, MaxPending: -1, SidecarMin: 1})
	if _, err := f.WriteAt("target", []byte("before"), 0); err != nil {
		t.Fatal(err)
	}
	gate := &commitGate{ModalClientClient: f.client.Stub, path: f.sidecarPath("target"), landed: make(chan struct{}), release: make(chan struct{})}
	f.client.Stub = gate
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	t.Cleanup(release)
	commitDone := make(chan error, 1)
	go func() { commitDone <- f.Flush("target") }()
	select {
	case <-gate.landed:
	case <-time.After(2 * time.Second):
		t.Fatal("old sidecar publication did not pause")
	}
	removeDone := make(chan error, 1)
	go func() { removeDone <- f.Remove("target") }()
	select {
	case err := <-removeDone:
		t.Fatalf("remove passed a live old sidecar writer: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	release()
	if err := <-commitDone; err != nil {
		t.Fatal(err)
	}
	if err := <-removeDone; err != nil {
		t.Fatal(err)
	}
	if _, err := f.Stat("target"); !os.IsNotExist(err) {
		t.Fatalf("old commit resurrected removed target: %v", err)
	}
	if _, err := f.WriteAt("target", []byte("newnew"), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Flush("target"); err != nil {
		t.Fatal(err)
	}
	data, err := f.client.ReadFile(context.Background(), f.vol.ID, "target", 1024)
	if err != nil || string(data) != "newnew" {
		t.Fatalf("old writer removed recreated successor: %q %v", data, err)
	}
	a, err := f.Stat("target")
	if err != nil {
		t.Fatal(err)
	}
	m := f.loadSidecar("target", 6, a.Mtime.Unix())
	if m == nil || string(m.Hashes[0]) != string(f.c.HashBlock([]byte("newnew"))) {
		t.Fatal("old sidecar writer/cleanup removed successor metadata")
	}
}
