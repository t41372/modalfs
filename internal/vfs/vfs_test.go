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
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/modalfake"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
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

// refuseCopy fails every copy, as Modal does when it's not allowed.
type refuseCopy struct{ pb.ModalClientClient }

func (refuseCopy) VolumeCopyFiles2(context.Context, *pb.VolumeCopyFiles2Request, ...grpc.CallOption) (*emptypb.Empty, error) {
	return nil, status.Error(codes.PermissionDenied, "copy refused")
}

// A rename onto a file with changes not on Modal yet, if it fails, leaves
// those changes as they were.
func TestFailedRenameKeepsReplacedFile(t *testing.T) {
	f := newFakeFS(t, Options{WriteBack: time.Hour, MaxPending: -1})
	f.put(t, "a", []byte("a"))
	f.put(t, "b", []byte("old b"))
	if _, err := f.WriteAt("b", []byte("NEW"), 0); err != nil {
		t.Fatal(err)
	}
	f.client.Stub = refuseCopy{f.client.Stub}
	if err := f.Rename("a", "b"); err == nil {
		t.Fatal("rename succeeded")
	}
	buf := make([]byte, 10)
	if n, err := f.ReadAt("b", buf, 0); err != nil || string(buf[:n]) != "NEW b" {
		t.Fatalf("b reads %q (%v) after the failed rename, want %q", buf[:n], err, "NEW b")
	}
	if len(f.PendingFiles()) != 1 {
		t.Fatal("b's changes are no longer pending")
	}
}

// commitGate holds the answer to the request that commits path until
// released: a commit that has landed on Modal but not ended yet.
type commitGate struct {
	pb.ModalClientClient
	path            string
	landed, release chan struct{}
	once            sync.Once
}

func (g *commitGate) VolumePutFiles2(ctx context.Context, in *pb.VolumePutFiles2Request, opts ...grpc.CallOption) (*pb.VolumePutFiles2Response, error) {
	r, err := g.ModalClientClient.VolumePutFiles2(ctx, in, opts...)
	if err == nil && len(r.GetMissingBlocks()) == 0 && in.GetFiles()[0].GetPath() == g.path {
		g.once.Do(func() {
			close(g.landed)
			<-g.release
		})
	}
	return r, err
}

// A rename onto a file whose commit is landing waits for that commit, which
// could otherwise bring the old file back, or delete the renamed one.
func TestRenameOntoFileBeingCommitted(t *testing.T) {
	f := newFakeFS(t, Options{WriteBack: time.Hour, MaxPending: -1})
	f.put(t, "a", []byte("renamed"))
	f.put(t, "b", []byte("old b"))
	if _, err := f.WriteAt("b", []byte("NEW"), 0); err != nil {
		t.Fatal(err)
	}
	g := &commitGate{ModalClientClient: f.client.Stub, path: "b", landed: make(chan struct{}), release: make(chan struct{})}
	f.client.Stub = g
	committed := make(chan error, 1)
	go func() { committed <- f.Flush("b") }()
	<-g.landed
	renamed := make(chan error, 1)
	go func() { renamed <- f.Rename("a", "b") }()
	select {
	case err := <-renamed:
		t.Fatalf("the rename didn't wait for the commit in flight (%v)", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(g.release)
	if err := <-committed; err != nil {
		t.Fatal(err)
	}
	if err := <-renamed; err != nil {
		t.Fatal(err)
	}
	f.invalidate("")
	if _, err := f.Stat("a"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a after the rename: %v", err)
	}
	buf := make([]byte, 10)
	if n, err := f.ReadAt("b", buf, 0); err != nil || string(buf[:n]) != "renamed" {
		t.Fatalf("b reads %q (%v), want %q", buf[:n], err, "renamed")
	}
}

// refuseRemove fails every removal.
type refuseRemove struct{ pb.ModalClientClient }

func (refuseRemove) VolumeRemoveFile2(context.Context, *pb.VolumeRemoveFile2Request, ...grpc.CallOption) (*emptypb.Empty, error) {
	return nil, status.Error(codes.PermissionDenied, "remove refused")
}

// A folder Modal didn't remove isn't reported as removed.
func TestFailedRmdirIsReported(t *testing.T) {
	f := newFakeFS(t, Options{})
	if err := f.Mkdir("d"); err != nil {
		t.Fatal(err)
	}
	f.client.Stub = refuseRemove{f.client.Stub}
	if err := f.Remove("d"); err == nil {
		t.Fatal("removing the folder succeeded, though Modal refused it")
	}
}

// slowBase holds every request for the blocks of path until released, and
// tells of each listing once it's read.
type slowBase struct {
	pb.ModalClientClient
	path    string
	release chan struct{}
	listed  chan struct{}
}

type readListing struct {
	grpc.ServerStreamingClient[pb.VolumeListFiles2Response]
	done func()
}

func (r readListing) Recv() (*pb.VolumeListFiles2Response, error) {
	m, err := r.ServerStreamingClient.Recv()
	if err == io.EOF {
		r.done()
	}
	return m, err
}

func (s *slowBase) VolumeGetFile2(ctx context.Context, in *pb.VolumeGetFile2Request, opts ...grpc.CallOption) (*pb.VolumeGetFile2Response, error) {
	if in.GetPath() == s.path {
		<-s.release
	}
	return s.ModalClientClient.VolumeGetFile2(ctx, in, opts...)
}

func (s *slowBase) VolumeListFiles2(ctx context.Context, in *pb.VolumeListFiles2Request, opts ...grpc.CallOption) (grpc.ServerStreamingClient[pb.VolumeListFiles2Response], error) {
	stream, err := s.ModalClientClient.VolumeListFiles2(ctx, in, opts...)
	if err != nil {
		return nil, err
	}
	return readListing{stream, func() {
		select {
		case s.listed <- struct{}{}:
		default:
		}
	}}, nil
}

// A commit that first reads the blocks it didn't change, and so commits
// long after it looked for a change on Modal, looks again before it
// commits, though Modal has every block and nothing is uploaded. The other
// version is the same size, so the block it read may be the other
// version's (here it's the same): the changes are set aside, not mixed
// into a conflict copy.
func TestReplacedWhileReadingTheBase(t *testing.T) {
	f := newFakeFS(t, Options{WriteBack: time.Hour, MaxPending: -1})
	ours := randBytes(blockSize) // on Modal already, so the commit uploads nothing
	f.put(t, "x", ours)
	base := randBytes(2 * blockSize)
	f.put(t, "f", base)
	time.Sleep(time.Second) // a change gets another mtime
	s := &slowBase{ModalClientClient: f.client.Stub, path: "f", release: make(chan struct{}), listed: make(chan struct{}, 1)}
	f.client.Stub = s
	if _, err := f.WriteAt("f", ours, 0); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.listed:
	default:
	}
	committed := make(chan error, 1)
	go func() { committed <- f.Flush("f") }()
	<-s.listed // the commit looked: no change yet
	theirs := append(randBytes(blockSize), base[blockSize:]...)
	f.put(t, "f", theirs)
	time.Sleep(1100 * time.Millisecond)
	close(s.release)
	if err := <-committed; err != nil {
		t.Fatal(err)
	}
	f.invalidate("")
	buf := make([]byte, len(theirs))
	if n, err := f.ReadAt("f", buf, 0); err != nil || !bytes.Equal(buf[:n], theirs) {
		t.Fatalf("the other version of f was overwritten (%d bytes, %v)", n, err)
	}
	ents, err := f.ReadDir("")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.Contains(e.Name, ".conflict-") {
			t.Fatalf("a conflict copy was made: %s", e.Name)
		}
	}
	if notes, _ := filepath.Glob(filepath.Join(f.opt.CacheDir, f.volID, "unsaved", "*", "README.txt")); len(notes) != 1 {
		t.Fatalf("the changes weren't set aside: %v", notes)
	}
}

// When every block a conflict copy keeps unchanged was known before the
// edit began, the copy is made though the other version is the same size,
// and it's exactly ours.
func TestSameSizeConflictCopy(t *testing.T) {
	f := newFakeFS(t, Options{WriteBack: time.Hour, MaxPending: -1})
	base := randBytes(2 * blockSize)
	f.put(t, "f", base)
	buf := make([]byte, len(base))
	if _, err := f.ReadAt("f", buf, 0); err != nil { // learns both blocks
		t.Fatal(err)
	}
	time.Sleep(time.Second) // a change gets another mtime
	if _, err := f.WriteAt("f", []byte("ours"), 10); err != nil {
		t.Fatal(err)
	}
	theirs := randBytes(len(base))
	f.put(t, "f", theirs)
	if err := f.Flush("f"); err != nil {
		t.Fatal(err)
	}
	ours := bytes.Clone(base)
	copy(ours[10:], "ours")
	f.invalidate("")
	if n, err := f.ReadAt("f", buf, 0); err != nil || !bytes.Equal(buf[:n], theirs) {
		t.Fatalf("the other version was overwritten (%d bytes, %v)", n, err)
	}
	ents, err := f.ReadDir("")
	if err != nil {
		t.Fatal(err)
	}
	var copies []string
	for _, e := range ents {
		if strings.Contains(e.Name, ".conflict-") {
			copies = append(copies, e.Name)
		}
	}
	if len(copies) != 1 {
		t.Fatalf("conflict copies %v, want 1", copies)
	}
	if n, err := f.ReadAt(copies[0], buf, 0); err != nil || !bytes.Equal(buf[:n], ours) {
		t.Fatalf("the conflict copy isn't our version (%d bytes, %v)", n, err)
	}
}

// refuseBlocks fails every request for a file's blocks.
type refuseBlocks struct{ pb.ModalClientClient }

func (refuseBlocks) VolumeGetFile2(context.Context, *pb.VolumeGetFile2Request, ...grpc.CallOption) (*pb.VolumeGetFile2Response, error) {
	return nil, status.Error(codes.PermissionDenied, "blocks refused")
}

// A truncate that fails, here because it can't read the block it cuts
// through, leaves the file as it was, pending writes past the cut too.
func TestFailedTruncateKeepsPendingTail(t *testing.T) {
	f := newFakeFS(t, Options{WriteBack: time.Hour, MaxPending: -1})
	f.put(t, "f", randBytes(2*blockSize))
	ours := randBytes(blockSize)
	if _, err := f.WriteAt("f", ours, blockSize); err != nil {
		t.Fatal(err)
	}
	f.client.Stub = refuseBlocks{f.client.Stub}
	if err := f.Truncate("f", blockSize/2); err == nil {
		t.Fatal("the truncate succeeded without the block it cuts through")
	}
	buf := make([]byte, blockSize)
	if n, err := f.ReadAt("f", buf, blockSize); err != nil || !bytes.Equal(buf[:n], ours) {
		t.Fatalf("after the failed truncate, the pending write past the cut reads back wrong (%d bytes, %v)", n, err)
	}
}

// A block downloaded again (here, dropped from the cache) that isn't the
// one already known for that version means the file was replaced with
// one of the same size: it's reported stale, not learned as that version.
func TestSameSizeReplacementIsStale(t *testing.T) {
	f := newFakeFS(t, Options{ReadAhead: -1})
	f.put(t, "f", randBytes(blockSize))
	buf := make([]byte, blockSize)
	if _, err := f.ReadAt("f", buf, 0); err != nil {
		t.Fatal(err)
	}
	a, _ := f.Stat("f")
	h := f.mdb.Hash("f", a.Size, a.Mtime.Unix(), 0)
	if err := os.Remove(f.cache.path(fmt.Sprintf("%x", h))); err != nil {
		t.Fatal(err)
	}
	f.put(t, "f", randBytes(blockSize)) // the listing still has the old version
	if _, err := f.ReadAt("f", buf, 0); !errors.Is(err, syscall.ESTALE) {
		t.Fatalf("reading the replaced file as the old version: %v, want ESTALE", err)
	}
	if got := f.mdb.Hash("f", a.Size, a.Mtime.Unix(), 0); !bytes.Equal(got, h) {
		t.Fatal("the replacement's block was learned as the old version's")
	}
}

// A conflict copy's name fits Modal's 255 bytes however long the name is,
// and keeps the extension.
func TestLongConflictName(t *testing.T) {
	f := newFakeFS(t, Options{})
	name := strings.Repeat("長", 80) + ".txt" // 244 bytes
	got := path.Base(f.conflictName("d/" + name))
	if len(got) > maxName || !utf8.ValidString(got) || !strings.HasSuffix(got, ".txt") || !strings.Contains(got, ".conflict-") {
		t.Fatalf("conflict copy named %q (%d bytes)", got, len(got))
	}
}

// Changes that can't be set aside aren't reported as set aside: the
// commit fails, to be tried again, and the changes stay.
func TestFailedParkIsAnError(t *testing.T) {
	f := newFakeFS(t, Options{WriteBack: time.Hour, MaxPending: -1})
	if err := f.Create("f", false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt("f", []byte("ours"), 0); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.opt.CacheDir, f.volID, "unsaved"), nil, 0o600); err != nil {
		t.Fatal(err) // a file where the folder for set-aside changes goes
	}
	st := f.files["f"]
	if err := st.park(&snapshot{}, errBaseLost); err == nil {
		t.Fatal("park succeeded without setting the changes aside")
	}
	if f.files["f"] != st || len(f.PendingFiles()) != 1 {
		t.Fatal("the changes that couldn't be set aside are no longer pending")
	}
}

// An edit that read part of the file from Modal while editing it, replaced
// meanwhile by a version of the same size, is set aside, not mixed into a
// conflict copy: what it read may be the other version's.
func TestSameSizeReplacementSetAside(t *testing.T) {
	f := newFakeFS(t, Options{WriteBack: time.Hour, MaxPending: -1})
	f.put(t, "f", randBytes(2*blockSize))
	time.Sleep(time.Second) // a change gets another mtime
	// a partial write: block 0 is read from Modal
	if _, err := f.WriteAt("f", []byte("ours"), 10); err != nil {
		t.Fatal(err)
	}
	theirs := randBytes(2 * blockSize)
	f.put(t, "f", theirs)
	if err := f.Flush("f"); err != nil {
		t.Fatal(err)
	}
	f.invalidate("")
	buf := make([]byte, len(theirs))
	if n, err := f.ReadAt("f", buf, 0); err != nil || !bytes.Equal(buf[:n], theirs) {
		t.Fatalf("the other version was overwritten (%d bytes, %v)", n, err)
	}
	ents, err := f.ReadDir("")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.Contains(e.Name, ".conflict-") {
			t.Fatalf("a conflict copy was made: %s", e.Name)
		}
	}
	if notes, _ := filepath.Glob(filepath.Join(f.opt.CacheDir, f.volID, "unsaved", "*", "README.txt")); len(notes) != 1 {
		t.Fatalf("the changes weren't set aside: %v", notes)
	}
}

// A block built from what was read of the base, then overwritten whole,
// holds only our bytes: a same-size replacement gets a conflict copy, as
// for any edit of ours alone.
func TestOverwrittenBlockIsOurs(t *testing.T) {
	f := newFakeFS(t, Options{WriteBack: time.Hour, MaxPending: -1})
	f.put(t, "f", randBytes(blockSize))
	time.Sleep(time.Second) // a change gets another mtime
	// a partial write: block 0 is read from Modal
	if _, err := f.WriteAt("f", []byte("partial"), 10); err != nil {
		t.Fatal(err)
	}
	ours := randBytes(blockSize)
	if _, err := f.WriteAt("f", ours, 0); err != nil {
		t.Fatal(err)
	}
	f.put(t, "f", randBytes(blockSize))
	if err := f.Flush("f"); err != nil {
		t.Fatal(err)
	}
	f.invalidate("")
	ents, err := f.ReadDir("")
	if err != nil {
		t.Fatal(err)
	}
	var copies []string
	for _, e := range ents {
		if strings.Contains(e.Name, ".conflict-") {
			copies = append(copies, e.Name)
		}
	}
	if len(copies) != 1 {
		t.Fatalf("conflict copies %v, want 1", copies)
	}
	buf := make([]byte, blockSize)
	if n, err := f.ReadAt(copies[0], buf, 0); err != nil || !bytes.Equal(buf[:n], ours) {
		t.Fatalf("the conflict copy isn't ours (%d bytes, %v)", n, err)
	}
}
