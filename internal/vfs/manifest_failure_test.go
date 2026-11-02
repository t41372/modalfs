package vfs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"github.com/t41372/modalfs/internal/modalfake"
	bolt "go.etcd.io/bbolt"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// A read-only DB is a deterministic disk-write failure while old records
// remain readable. Reopening is protected by the same mutex as the flusher.
func manifestDiskReadOnly(t *testing.T, d *manifestDB) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	p := d.db.Path()
	if err := d.db.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	d.db, err = bolt.Open(p, 0o600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
}

func TestManifestInvalidationFailureQuarantinesDisk(t *testing.T) {
	d, err := openManifestDB(filepath.Join(t.TempDir(), "manifest.db"), "volume")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, p := range []string{"target", "target/child", "neighbor"} {
		d.Put(p, &manifest{Size: 10, Mtime: 1, Hashes: [][]byte{hashOf(p)}})
	}
	manifestDiskReadOnly(t, d)
	reported := make(chan error, 2)
	d.setErrorHandler(func(err error) {
		// The callback must be able to use the cache without a lock cycle.
		_ = d.Get("neighbor", 10, 1)
		reported <- err
	})
	if err := d.DeleteTree("target"); !errors.Is(err, bolt.ErrDatabaseReadOnly) {
		t.Fatalf("invalidation error = %v", err)
	}
	for _, p := range []string{"target", "target/child"} {
		if d.Get(p, 10, 1) != nil {
			t.Fatalf("failed disk delete revived %q", p)
		}
	}
	if d.Get("neighbor", 10, 1) == nil {
		t.Fatal("unrelated RAM record was discarded")
	}
	// Prove stale records really remain on disk, but can never enter RAM.
	d.mu.Lock()
	d.dropLocked("neighbor")
	err = d.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket(d.bucket).Get([]byte("target")) == nil {
			t.Fatal("test did not retain the stale disk record")
		}
		return nil
	})
	d.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if d.Get("neighbor", 10, 1) != nil {
		t.Fatal("quarantined disk fallback was consulted")
	}
	// Once disk fallback is disabled, later invalidations are safe RAM-only
	// operations; the owner need not stop editing after the first warning.
	if err := d.DeleteTree("neighbor"); err != nil {
		t.Fatal(err)
	}
	d.max = 1000
	for i := 0; i < 100; i++ {
		d.SetHash(strings.Repeat("x", i+1), 10, 1, 0, hashOf("fresh"))
	}
	d.mu.Lock()
	if d.bytes > d.max && d.lru.Len() > 1 || len(d.pending) != 0 {
		t.Fatalf("RAM cache grew after quarantine: bytes=%d max=%d entries=%d pending=%d", d.bytes, d.max, d.lru.Len(), len(d.pending))
	}
	d.mu.Unlock()
	select {
	case err := <-reported:
		if !errors.Is(err, bolt.ErrDatabaseReadOnly) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cache failure was not reported")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if len(reported) != 0 {
		t.Fatal("cache failure reported more than once")
	}
}

func TestManifestWriteFailureKeepsBoundedRAMOnly(t *testing.T) {
	d, err := openManifestDB(filepath.Join(t.TempDir(), "manifest.db"), "volume")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.Put("old", &manifest{Size: 10, Mtime: 1, Hashes: [][]byte{hashOf("old")}})
	manifestDiskReadOnly(t, d)
	d.Put("new", &manifest{Size: 10, Mtime: 1, Hashes: [][]byte{hashOf("new")}})
	if !bytes.Equal(d.Hash("new", 10, 1, 0), hashOf("new")) {
		t.Fatal("valid new RAM hash lost after disk write failure")
	}
	d.mu.Lock()
	d.dropLocked("old")
	d.mu.Unlock()
	if d.Get("old", 10, 1) != nil {
		t.Fatal("failed batch write left disk fallback enabled")
	}
}

// Restoring a valid earlier DB image models a NoSync crash rollback. It
// must not resurrect a known-invalid mapping even with identical size/mtime.
func TestManifestOpenDiscardsRolledBackAssociations(t *testing.T) {
	p := filepath.Join(t.TempDir(), "manifest.db")
	backup := p + ".before-invalidation"
	d, err := openManifestDB(p, "volume")
	if err != nil {
		t.Fatal(err)
	}
	d.Put("target", &manifest{Size: 8, Mtime: 1, Hashes: [][]byte{hashOf("old-data")}})
	err = d.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucket([]byte("other-volume"))
		if err != nil {
			return err
		}
		return b.Put([]byte("keep"), []byte("untouched"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.db.View(func(tx *bolt.Tx) error { return tx.CopyFile(backup, 0o600) }); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteTree("target"); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, old, 0o600); err != nil {
		t.Fatal(err)
	}
	d, err = openManifestDB(p, "volume")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Get("target", 8, 1) != nil {
		t.Fatal("NoSync rollback revived a known-stale path/hash mapping")
	}
	if err := d.db.View(func(tx *bolt.Tx) error {
		if got := tx.Bucket([]byte("other-volume")).Get([]byte("keep")); string(got) != "untouched" {
			t.Fatalf("clearing this volume touched another bucket: %q", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type cacheFailureCopy struct {
	pb.ModalClientClient
	fake   *modalfake.Server
	volume string
	mtime  int64
	after  func()
}

func (r cacheFailureCopy) VolumeCopyFiles2(ctx context.Context, req *pb.VolumeCopyFiles2Request, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	out, err := r.ModalClientClient.VolumeCopyFiles2(ctx, req, opts...)
	if err == nil {
		err = r.fake.Seed(r.volume, modalfake.SeedEntry{Path: "target", Data: []byte("new-data"), Mtime: r.mtime})
		if r.after != nil {
			r.after()
		}
	}
	return out, err
}

func TestRenameCacheInvalidationFailureBeforePublication(t *testing.T) {
	f := newFakeFS(t, Options{ReadAhead: -1})
	f.put(t, "source", []byte("new-data"))
	f.put(t, "target", []byte("old-data"))
	buf := make([]byte, 8)
	if _, err := f.ReadAt("target", buf, 0); err != nil {
		t.Fatal(err)
	}
	a, err := f.Stat("target")
	if err != nil {
		t.Fatal(err)
	}
	f.client.Stub = cacheFailureCopy{ModalClientClient: f.client.Stub, fake: f.fake, volume: f.vol.ID, mtime: a.Mtime.Unix()}
	manifestDiskReadOnly(t, f.mdb)
	if err := f.Rename("source", "target"); !errors.Is(err, bolt.ErrDatabaseReadOnly) || RenameMayHaveChangedDestination(err) {
		t.Fatalf("rename must fail before data publication: %v", err)
	}
	for p, want := range map[string]string{"source": "new-data", "target": "old-data"} {
		if _, err := f.ReadAt(p, buf, 0); err != nil || string(buf) != want {
			t.Fatalf("%s after refused rename: %q, %v", p, buf, err)
		}
	}
	// Quarantined fallback is safe: retry uses RAM-only metadata and must
	// never serve the still-present old target DB record after Copy.
	if err := f.Rename("source", "target"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ReadAt("target", buf, 0); err != nil || string(buf) != "new-data" {
		t.Fatalf("retried rename: %q, %v", buf, err)
	}
}

func TestRenameCacheFailureAfterPublicationKeepsSuccessfulOutcome(t *testing.T) {
	warnings := make(chan string, 1)
	f := newFakeFS(t, Options{ReadAhead: -1, OnEvent: func(kind, p, detail string) {
		if kind == "unreadable" && p == "local path/hash cache" {
			warnings <- detail
		}
	}})
	f.put(t, "source", []byte("new-data"))
	f.put(t, "target", []byte("old-data"))
	buf := make([]byte, 8)
	if _, err := f.ReadAt("target", buf, 0); err != nil {
		t.Fatal(err)
	}
	a, err := f.Stat("target")
	if err != nil {
		t.Fatal(err)
	}
	// The before-publication delete succeeds. Disk fails during Copy; a
	// detached/background cache record then survives only on disk.
	f.client.Stub = cacheFailureCopy{ModalClientClient: f.client.Stub, fake: f.fake, volume: f.vol.ID, mtime: a.Mtime.Unix(), after: func() {
		f.mdb.Put("target", &manifest{Size: 8, Mtime: a.Mtime.Unix(), Hashes: f.c.HashBlocks([]byte("old-data"))})
		manifestDiskReadOnly(t, f.mdb)
	}}
	if err := f.Rename("source", "target"); err != nil {
		t.Fatalf("completed move reported failure: %v", err)
	}
	if _, err := f.Stat("source"); !os.IsNotExist(err) {
		t.Fatalf("source was not removed: %v", err)
	}
	if _, err := f.ReadAt("target", buf, 0); err != nil || string(buf) != "new-data" {
		t.Fatalf("postpublication cache failure: %q, %v", buf, err)
	}
	select {
	case detail := <-warnings:
		if !strings.Contains(detail, "pending writes are preserved") || !strings.Contains(detail, "Remount") {
			t.Fatalf("warning lacks preservation/recovery guidance: %s", detail)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("postpublication cache failure was silent")
	}
}
