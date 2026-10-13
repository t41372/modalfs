package vfs

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/modalfake"
	"google.golang.org/grpc"
)

// fakeFS is an FS on an in-process fake Modal: no account, no mount.
type fakeFS struct {
	*FS
	fake   *modalfake.Server
	client *modalapi.Client
	vol    modalapi.VolumeInfo
}

func newFakeFS(t testing.TB, opt Options) *fakeFS {
	t.Helper()
	dir := t.TempDir()
	fake, err := modalfake.Start(filepath.Join(dir, "fake"), modalfake.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fake.Close)
	c, err := modalapi.New(modalapi.Config{ServerURL: fake.URL, TokenID: "ak-fake", TokenSecret: "as-fake"})
	if err != nil {
		t.Fatal(err)
	}
	vol, err := c.LookupVolume(context.Background(), "test", true)
	if err != nil {
		t.Fatal(err)
	}
	opt.CacheDir = filepath.Join(dir, "cache")
	if opt.CacheMaxBytes == 0 {
		opt.CacheMaxBytes = 1 << 30
	}
	opt.Logger = slog.New(slog.DiscardHandler)
	v, err := New(c, vol, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	return &fakeFS{FS: v, fake: fake, client: c, vol: vol}
}

func (f *fakeFS) put(t testing.TB, name string, data []byte) {
	t.Helper()
	_, err := f.client.Put(context.Background(), f.vol.ID, []modalapi.PutFile{{
		Path: name, Size: int64(len(data)), Hashes: modalapi.HashBlocks(data),
		Block: func(i int) ([]byte, error) { return data[i*blockSize : min((i+1)*blockSize, len(data))], nil },
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

// waitServed waits for the fake's download count to settle.
func waitServed(f *fakeFS) int64 {
	last := f.fake.Served()
	for {
		time.Sleep(200 * time.Millisecond)
		n := f.fake.Served()
		if n == last {
			return n
		}
		last = n
	}
}

// Reads across block boundaries return the right bytes, from the network
// and then from the cache, and stop at the end of the file.
func TestReadRemoteRanges(t *testing.T) {
	f := newFakeFS(t, Options{ReadAhead: -1})
	data := randBytes(2*blockSize + 12345)
	f.put(t, "f", data)
	for pass := 0; pass < 2; pass++ {
		for _, r := range [][2]int64{{0, 100}, {blockSize - 10, 20}, {blockSize - 10, blockSize + 100}, {2*blockSize + 12000, 1000}, {0, int64(len(data))}} {
			buf := make([]byte, r[1])
			n, err := f.ReadAt("f", buf, r[0])
			want := data[r[0]:min(r[0]+r[1], int64(len(data)))]
			if err != nil || !bytes.Equal(buf[:n], want) {
				t.Fatalf("pass %d: read [%d,+%d): n=%d err=%v", pass, r[0], r[1], n, err)
			}
		}
	}
	if n, err := f.ReadAt("f", make([]byte, 10), int64(len(data))); n != 0 || err != nil {
		t.Fatalf("read at EOF: %d, %v", n, err)
	}
	if served := f.fake.Served(); served != int64(len(data)) {
		t.Fatalf("downloaded %d bytes for a %d byte file read twice", served, len(data))
	}
}

// A cached block that went short on disk is dropped and fetched again,
// not served or looped on.
func TestReadRemoteShortCachedBlock(t *testing.T) {
	f := newFakeFS(t, Options{ReadAhead: -1})
	data := randBytes(blockSize + 100)
	f.put(t, "f", data)
	buf := make([]byte, len(data))
	if _, err := f.ReadAt("f", buf, 0); err != nil {
		t.Fatal(err)
	}
	a, _ := f.Stat("f")
	h := f.mdb.Hash("f", a.Size, a.Mtime.Unix(), 0)
	if err := truncateFile(f.cache.path(fmt.Sprintf("%x", h)), 100); err != nil {
		t.Fatal(err)
	}
	clear(buf)
	if n, err := f.ReadAt("f", buf, blockSize-50); err != nil || n != 150 || !bytes.Equal(buf[:n], data[blockSize-50:]) {
		t.Fatalf("read after damage: n=%d err=%v", n, err)
	}
}

// Read-ahead starts only after three blocks were read in order (the NFS
// client's own read-ahead covers two), never for the first block of a file.
func TestReadAheadAfterThreeBlocks(t *testing.T) {
	f := newFakeFS(t, Options{ReadAhead: 2})
	data := randBytes(8 * blockSize)
	f.put(t, "f", data)
	buf := make([]byte, 64<<10)
	for i, want := range []int64{1, 2, 5} { // blocks 0, 1, then 2 (+ 3 and 4 read ahead)
		if _, err := f.ReadAt("f", buf, int64(i)*blockSize); err != nil {
			t.Fatal(err)
		}
		if n := waitServed(f); n != want*blockSize {
			t.Fatalf("after reading block %d, %d blocks were downloaded; want %d", i, n/blockSize, want)
		}
	}
	// a jump elsewhere starts no read-ahead
	if _, err := f.ReadAt("f", buf, 7*blockSize); err != nil {
		t.Fatal(err)
	}
	if n := waitServed(f); n != 6*blockSize {
		t.Fatalf("a random read downloaded %d blocks in all; want 6", n/blockSize)
	}
}

// With every read-ahead slot taken, a sequential read downloads only what
// it reads.
func TestReadAheadSlotsFull(t *testing.T) {
	f := newFakeFS(t, Options{ReadAhead: 4})
	f.put(t, "f", randBytes(6*blockSize))
	for f.cache.startPrefetch() {
	}
	buf := make([]byte, 64<<10)
	for i := int64(0); i < 3; i++ {
		if _, err := f.ReadAt("f", buf, i*blockSize); err != nil {
			t.Fatal(err)
		}
	}
	if n := waitServed(f); n != 3*blockSize {
		t.Fatalf("downloaded %d blocks with no read-ahead slots; want 3", n/blockSize)
	}
}

func TestReadTracker(t *testing.T) {
	var rt readTracker
	steps := []struct {
		p    string
		idx  int64
		want bool
	}{
		{"a", 0, false}, {"a", 0, false}, {"a", 1, false}, {"a", 1, false}, {"a", 2, true}, {"a", 3, true},
		{"b", 5, false}, {"b", 6, false}, {"b", 7, true}, // another file
		{"a", 40, false}, {"a", 4, true}, // a second reader doesn't reset the first
		{"a", 41, false}, {"a", 42, true},
		{"a", 10, false}, {"a", 11, false}, {"a", 12, true}, // a jump starts a new run
	}
	for i, s := range steps {
		if got := rt.sequential(s.p, s.idx); got != s.want {
			t.Fatalf("step %d (%s block %d): %v, want %v", i, s.p, s.idx, got, s.want)
		}
	}
	for i := 0; i < 3*readFiles; i++ {
		rt.sequential(fmt.Sprint(i), 0)
	}
	if len(rt.files) > readFiles || rt.lru.Len() > readFiles {
		t.Fatalf("tracker holds %d files", len(rt.files))
	}
}

// gatedList holds the next directory listing after the server answered it,
// until released: a listing that started before a change but ends after it.
type gatedList struct {
	pb.ModalClientClient
	mu     sync.Mutex
	gate   chan struct{}
	listed chan struct{}
}

func (g *gatedList) VolumeListFiles2(ctx context.Context, in *pb.VolumeListFiles2Request, opts ...grpc.CallOption) (grpc.ServerStreamingClient[pb.VolumeListFiles2Response], error) {
	s, err := g.ModalClientClient.VolumeListFiles2(ctx, in, opts...)
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	gate := g.gate
	g.gate = nil
	g.mu.Unlock()
	if gate == nil {
		return s, nil
	}
	var msgs []*pb.VolumeListFiles2Response
	for {
		m, err := s.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	close(g.listed)
	<-gate
	return &heldStream{ClientStream: s, msgs: msgs}, nil
}

type heldStream struct {
	grpc.ClientStream
	msgs []*pb.VolumeListFiles2Response
}

func (h *heldStream) Recv() (*pb.VolumeListFiles2Response, error) {
	if len(h.msgs) == 0 {
		return nil, io.EOF
	}
	m := h.msgs[0]
	h.msgs = h.msgs[1:]
	return m, nil
}

// A fresh listing after an invalidation doesn't join a listing that began
// before it, and the older listing isn't cached when it ends.
func TestFreshListingAfterInvalidate(t *testing.T) {
	f := newFakeFS(t, Options{})
	f.put(t, "d/old", []byte("x"))
	release := make(chan struct{})
	g := &gatedList{ModalClientClient: f.client.Stub, gate: release, listed: make(chan struct{})}
	f.client.Stub = g
	stale := make(chan map[string]modalapi.Entry, 1)
	go func() {
		m, err := f.listDir("d", false)
		if err != nil {
			t.Error(err)
		}
		stale <- m
	}()
	<-g.listed // the old listing has its answer, without "new"
	f.put(t, "d/new", []byte("y"))
	f.invalidate("d")
	fresh := make(chan map[string]modalapi.Entry, 1)
	go func() {
		m, err := f.listDir("d", true)
		if err != nil {
			t.Error(err)
		}
		fresh <- m
	}()
	select {
	case m := <-fresh:
		if _, ok := m["new"]; !ok {
			t.Error("fresh listing misses a file added before it was asked for")
		}
	case <-time.After(5 * time.Second):
		t.Error("fresh listing joined the one in flight from before the change")
		defer func() { <-fresh }()
	}
	close(release)
	if m := <-stale; m["new"].Path != "" {
		t.Fatal("the held listing wasn't the old one; the test proves nothing")
	}
	if _, err := f.Stat("d/new"); err != nil {
		t.Fatalf("after the old listing ended, the cache lost the new file: %v", err)
	}
}

// Expired listings are swept once there are many, so the cache holds
// about what was listed within one TTL.
func TestDirSweep(t *testing.T) {
	f := newFakeFS(t, Options{DirCacheTTL: 50 * time.Millisecond})
	for round := 0; round < 3; round++ {
		for i := 0; i < dirSweepMin; i++ {
			if _, err := f.listDir(fmt.Sprintf("r%dd%d", round, i), false); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		}
		time.Sleep(60 * time.Millisecond)
	}
	f.mu.Lock()
	n := len(f.dirs)
	f.mu.Unlock()
	if n > dirSweepMin+1 {
		t.Fatalf("%d listings cached after listing %d folders", n, 3*dirSweepMin)
	}
}

func truncateFile(p string, size int64) error { return os.Truncate(p, size) }

// PendingFiles lists what Status counts: the changed files, with the changed bytes.
func TestPendingFilesMatchStatus(t *testing.T) {
	f := newFakeFS(t, Options{WriteBack: time.Hour})
	if got := f.PendingFiles(); len(got) != 0 {
		t.Fatalf("before writing: %v", got)
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := f.Create(name, false); err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteAt(name, []byte("hello"), 0); err != nil {
			t.Fatal(err)
		}
	}
	got, st := f.PendingFiles(), f.Status()
	if len(got) != 2 || st.PendingFiles != 2 {
		t.Fatalf("pending %v, status counts %d", got, st.PendingFiles)
	}
	var sum int64
	for _, p := range got {
		sum += p.Bytes
		if p.Size != 5 || p.Since == 0 {
			t.Fatalf("pending file %+v", p)
		}
	}
	if sum != st.PendingBytes {
		t.Fatalf("pending bytes %d, status says %d", sum, st.PendingBytes)
	}
}
