package vfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"github.com/t41372/modalfs/internal/modalfake"
	bolt "go.etcd.io/bbolt"
	"google.golang.org/grpc"
)

func remoteDigest(t *testing.T, f *fakeFS, p string) string {
	t.Helper()
	entries, err := f.fake.Inspect(f.vol.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Path == p {
			return e.SHA256
		}
	}
	t.Fatalf("remote file %s is missing", p)
	return ""
}

func TestCommitCacheInvalidationFailureRetainsJournal(t *testing.T) {
	f := newFakeFS(t, Options{ReadAhead: -1, WriteBack: time.Hour, MaxPending: -1})
	f.put(t, "target", []byte("old-data"))
	buf := make([]byte, 8)
	if _, err := f.ReadAt("target", buf, 0); err != nil {
		t.Fatal(err)
	}
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
	if err := f.Flush("target"); !errors.Is(err, bolt.ErrDatabaseReadOnly) {
		t.Fatalf("commit must expose prepublication cache failure: %v", err)
	}
	if got := remoteDigest(t, f, "target"); got != fmt.Sprintf("%x", hashOf("old-data")) {
		t.Fatalf("refused commit changed remote data: %s", got)
	}
	if _, err := os.Stat(journal); err != nil {
		t.Fatalf("failed commit lost journal: %v", err)
	}
	if pending := f.PendingFiles(); len(pending) != 1 || pending[0].Path != "target" {
		t.Fatalf("failed commit lost pending state: %+v", pending)
	}
	if _, err := f.ReadAt("target", buf, 0); err != nil || string(buf) != "new-data" {
		t.Fatalf("failed commit lost local data: %q, %v", buf, err)
	}
	if err := f.Flush("target"); err != nil {
		t.Fatalf("RAM-only retry failed: %v", err)
	}
	if got := remoteDigest(t, f, "target"); got != fmt.Sprintf("%x", hashOf("new-data")) || len(f.PendingFiles()) != 0 {
		t.Fatalf("retry did not commit retained writes: digest=%s pending=%+v", got, f.PendingFiles())
	}
}

type cacheFailureCommit struct {
	pb.ModalClientClient
	once  sync.Once
	after func()
}

func (r *cacheFailureCommit) VolumePutFiles2(ctx context.Context, req *pb.VolumePutFiles2Request, opts ...grpc.CallOption) (*pb.VolumePutFiles2Response, error) {
	out, err := r.ModalClientClient.VolumePutFiles2(ctx, req, opts...)
	if err == nil && len(out.GetMissingBlocks()) == 0 && len(req.GetFiles()) == 1 && req.GetFiles()[0].GetPath() == "target" {
		r.once.Do(r.after)
	}
	return out, err
}

func TestCommitCacheFailureAfterPublicationKeepsSuccessfulOutcome(t *testing.T) {
	f := newFakeFS(t, Options{ReadAhead: -1, WriteBack: time.Hour, MaxPending: -1})
	f.put(t, "target", []byte("old-data"))
	buf := make([]byte, 8)
	if _, err := f.ReadAt("target", buf, 0); err != nil {
		t.Fatal(err)
	}
	a, err := f.Stat("target")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteExistingAt("target", []byte("new-data"), 0); err != nil {
		t.Fatal(err)
	}
	f.client.Stub = &cacheFailureCommit{ModalClientClient: f.client.Stub, after: func() {
		if err := f.fake.Seed(f.vol.ID, modalfake.SeedEntry{Path: "target", Data: []byte("new-data"), Mtime: a.Mtime.Unix()}); err != nil {
			t.Fatal(err)
		}
		f.mdb.Put("target", &manifest{Size: 8, Mtime: a.Mtime.Unix(), Hashes: f.c.HashBlocks([]byte("old-data"))})
		manifestDiskReadOnly(t, f.mdb)
	}}
	if err := f.Flush("target"); err != nil {
		t.Fatalf("published commit reported cache failure: %v", err)
	}
	if pending := f.PendingFiles(); len(pending) != 0 {
		t.Fatalf("published commit stayed pending: %+v", pending)
	}
	if _, err := f.ReadAt("target", buf, 0); err != nil || string(buf) != "new-data" {
		t.Fatalf("published manifest served stale bytes: %q, %v", buf, err)
	}
	// A RAM miss cannot reload the older same-version row left on disk.
	f.mdb.mu.Lock()
	f.mdb.dropLocked("target")
	f.mdb.mu.Unlock()
	if _, err := f.ReadAt("target", buf, 0); err != nil || string(buf) != "new-data" {
		t.Fatalf("RAM eviction revived stale disk hashes: %q, %v", buf, err)
	}
}
