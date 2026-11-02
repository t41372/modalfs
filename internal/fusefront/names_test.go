package fusefront

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	gofs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"github.com/t41372/modalfs/internal/encryption"
	"github.com/t41372/modalfs/internal/modalapi"
	"github.com/t41372/modalfs/internal/modalfake"
	"github.com/t41372/modalfs/internal/vfs"
	"github.com/t41372/modalfs/internal/volume"
	"golang.org/x/text/unicode/norm"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type testFrontend struct {
	v      *vfs.FS
	remote *volume.Volume
	client *modalapi.Client
	vol    modalapi.VolumeInfo
	root   *node
	wire   fuse.RawFileSystem
	events func() []string
}
type testNode struct {
	n  *node
	id uint64
}

// Inject a small deterministic client namespace so these adapter tests also
// run on Linux. Foundation's exact key has separate native macOS coverage.
func decomposedName(name string) (string, error) { return norm.NFD.String(name), nil }

func newTestFrontend(t *testing.T, encrypted bool, key func(string) (string, error)) *testFrontend {
	t.Helper()
	fake, err := modalfake.Start(filepath.Join(t.TempDir(), "fake"), modalfake.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(fake.Close)
	client, err := modalapi.New(modalapi.Config{ServerURL: fake.URL, TokenID: "ak-fake", TokenSecret: "as-fake"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	vol, err := client.LookupVolume(context.Background(), "fuse-namespace", true)
	if err != nil {
		t.Fatal(err)
	}
	remote := &volume.Volume{Client: client, ID: vol.ID}
	if encrypted {
		remote.Cipher = encryption.New(encryption.NewKey())
		if err := remote.WriteMarker(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	var eventMu sync.Mutex
	var events []string
	onEvent := func(kind, p, detail string) {
		eventMu.Lock()
		events = append(events, kind+":"+detail)
		eventMu.Unlock()
	}
	v, err := vfs.New(client, vol, vfs.Options{CacheDir: t.TempDir(), Cipher: remote.Cipher, NameKey: key, WriteBack: time.Hour, MaxPending: -1, DirCacheTTL: time.Millisecond, OnEvent: onEvent})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	root := newRoot(v)
	wire := gofs.NewNodeFS(root, &gofs.Options{})
	return &testFrontend{v: v, remote: remote, client: client, vol: vol, root: root, wire: wire, events: func() []string { eventMu.Lock(); defer eventMu.Unlock(); return append([]string(nil), events...) }}
}
func (f *testFrontend) put(t *testing.T, name, data string) {
	t.Helper()
	if err := f.remote.WriteFile(context.Background(), name, []byte(data)); err != nil {
		t.Fatal(err)
	}
}
func (f *testFrontend) lookup(t *testing.T, parent testNode, name string) testNode {
	t.Helper()
	var out fuse.EntryOut
	if status := f.wire.Lookup(nil, &fuse.InHeader{NodeId: parent.id}, name, &out); status != fuse.OK {
		t.Fatalf("lookup %q: %v", name, status)
	}
	child := parent.n.GetChild(name)
	if child == nil {
		t.Fatalf("lookup didn't attach %q", name)
	}
	return testNode{child.Operations().(*node), out.NodeId}
}
func (f *testFrontend) rootNode() testNode { return testNode{f.root, 1} }
func readNode(n *node) (string, syscall.Errno) {
	data, status := n.Read(context.Background(), nil, make([]byte, 128), 0)
	if status != 0 {
		return "", status
	}
	defer data.Done()
	got, readStatus := data.Bytes(nil)
	return string(got), syscall.Errno(readStatus)
}
func expectRead(t *testing.T, n *node, want string) {
	t.Helper()
	got, status := readNode(n)
	if status != 0 || got != want {
		t.Fatalf("read=%q errno=%v want=%q", got, status, want)
	}
}
func (f *testFrontend) rename(t *testing.T, parent testNode, old string, next testNode, name string) {
	t.Helper()
	in := &fuse.RenameIn{InHeader: fuse.InHeader{NodeId: parent.id}, Newdir: next.id}
	if status := f.wire.Rename(nil, in, old, name); status != fuse.OK {
		t.Fatalf("rename %q -> %q: %v", old, name, status)
	}
}
func (f *testFrontend) readRemote(t *testing.T, name, want string) {
	t.Helper()
	got, err := f.remote.ReadFile(context.Background(), name, 1<<20)
	if err != nil || string(got) != want {
		t.Fatalf("remote %q=%q %v want=%q", name, got, err, want)
	}
}

func TestClientNamesAndBoundDirectoryRename(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		t.Run(fmt.Sprintf("encrypted=%v", encrypted), func(t *testing.T) {
			f := newTestFrontend(t, encrypted, decomposedName)
			const original = "café/subñ/résumé.txt"
			f.put(t, original, "original")
			root := f.rootNode()
			dir := f.lookup(t, root, "cafe\u0301")
			composed := f.lookup(t, root, "café")
			if composed.id != dir.id {
				t.Fatal("equivalent directory names have different inode identities")
			}
			sub := f.lookup(t, dir, "subn\u0303")
			leaf := f.lookup(t, sub, "re\u0301sume\u0301.txt")
			expectRead(t, leaf.n, "original")
			if got, status := leaf.n.object.current(); status != 0 || got != original {
				t.Fatalf("bound raw path %q, %v", got, status)
			}
			if _, status := leaf.n.Write(context.Background(), nil, []byte("updated!"), 0); status != 0 {
				t.Fatal(status)
			}
			if err := f.v.Flush(original); err != nil {
				t.Fatal(err)
			}
			f.readRemote(t, original, "updated!")
			entries, err := f.remote.List(context.Background(), "café/subñ", false)
			if err != nil || len(entries) != 1 || entries[0].Path != original {
				t.Fatalf("write changed original spelling: %+v %v", entries, err)
			}
			if encrypted {
				stored, _ := f.remote.Path(original)
				alias, _ := f.remote.Path(norm.NFD.String(original))
				if stored == alias {
					t.Fatal("encrypted fixture must distinguish input bytes")
				}
				if _, _, err := f.client.BlockURLs(context.Background(), f.vol.ID, alias, 0, 1); !modalapi.IsNotFound(err) {
					t.Fatalf("write created a second encrypted spelling: %v", err)
				}
			}
			renamed := "renomme\u0301"
			f.rename(t, root, "cafe\u0301", root, renamed)
			const suffix = "/subñ/résumé.txt"
			wantPath := renamed + suffix
			if got, status := leaf.n.object.current(); status != 0 || got != wantPath {
				t.Fatalf("descendant binding after rename=%q %v", got, status)
			}
			expectRead(t, leaf.n, "updated!")
			if _, status := leaf.n.Write(context.Background(), nil, []byte("renamed!"), 0); status != 0 {
				t.Fatal(status)
			}
			if err := f.v.Flush(wantPath); err != nil {
				t.Fatal(err)
			}
			f.readRemote(t, wantPath, "renamed!")
			if _, err := f.remote.ReadFile(context.Background(), original, 1<<20); !modalapi.IsNotFound(err) {
				t.Fatalf("old raw directory remained: %v", err)
			}
		})
	}
}

func TestBoundNodeDoesNotFollowNormalizedSuccessor(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		t.Run(fmt.Sprintf("encrypted=%v", encrypted), func(t *testing.T) {
			f := newTestFrontend(t, encrypted, decomposedName)
			const original = "café.txt"
			alias := norm.NFD.String(original)
			f.put(t, original, "original")
			old := f.lookup(t, f.rootNode(), alias)
			if err := f.remote.Remove(context.Background(), original, false); err != nil {
				t.Fatal(err)
			}
			f.put(t, alias, "successor")
			if got, status := readNode(old.n); status == 0 && got == "successor" {
				t.Fatal("old node followed a normalized successor")
			}
			_, status := old.n.Write(context.Background(), nil, []byte("old edit"), 0)
			if status == 0 {
				if err := f.v.Flush(original); err != nil {
					t.Fatal(err)
				}
			}
			f.readRemote(t, alias, "successor")
			if got, _ := old.n.object.current(); got != original {
				t.Fatalf("raw identity changed to %q", got)
			}
		})
	}
}

func TestCreatePreservesExistingAndExclusiveNames(t *testing.T) {
	f := newTestFrontend(t, false, decomposedName)
	f.put(t, "café.txt", "original")
	root := f.root
	_, _, _, status := root.Create(context.Background(), "cafe\u0301.txt", syscall.O_CREAT|syscall.O_RDWR, 0o644, &fuse.EntryOut{})
	if status != 0 {
		t.Fatal(status)
	}
	f.readRemote(t, "café.txt", "original")
	if _, _, _, status := root.Create(context.Background(), "cafe\u0301.txt", syscall.O_CREAT|syscall.O_EXCL|syscall.O_RDWR, 0o644, &fuse.EntryOut{}); status != syscall.EEXIST {
		t.Fatalf("exclusive create: %v", status)
	}
}

func TestBoundDirectoryDoesNotFollowNormalizedSuccessor(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		t.Run(fmt.Sprintf("encrypted=%v", encrypted), func(t *testing.T) {
			f := newTestFrontend(t, encrypted, decomposedName)
			f.put(t, "café/child", "original")
			oldDir := f.lookup(t, f.rootNode(), "cafe\u0301")
			oldFile := f.lookup(t, oldDir, "child")
			if err := f.remote.Remove(context.Background(), "café", true); err != nil {
				t.Fatal(err)
			}
			f.put(t, "cafe\u0301/child", "successor")
			if got, code := readNode(oldFile.n); code == 0 && got == "successor" {
				t.Fatal("old descendant followed a replacement parent")
			}
			if _, code := oldDir.n.Lookup(context.Background(), "child", &fuse.EntryOut{}); code == 0 {
				t.Fatal("lookup through old parent followed a replacement directory")
			}
			if _, _, _, code := oldDir.n.Create(context.Background(), "new", syscall.O_CREAT|syscall.O_EXCL|syscall.O_RDWR, 0o644, &fuse.EntryOut{}); code == 0 {
				t.Fatal("create through old parent modified a replacement directory")
			}
			f.readRemote(t, "cafe\u0301/child", "successor")
		})
	}
}

func TestReaddirKeepsUnrelatedNamesWhenOtherNamesCollide(t *testing.T) {
	f := newTestFrontend(t, false, decomposedName)
	f.put(t, "café", "one")
	f.put(t, "cafe\u0301", "two")
	f.put(t, "notes.txt", "notes")
	stream, status := f.root.Readdir(context.Background())
	if status != 0 {
		t.Fatal(status)
	}
	defer stream.Close()
	var names []string
	for stream.HasNext() {
		entry, status := stream.Next()
		if status != 0 {
			t.Fatal(status)
		}
		names = append(names, entry.Name)
	}
	if len(names) != 1 || names[0] != "notes.txt" {
		t.Fatalf("visible names=%q", names)
	}
	var out fuse.EntryOut
	if _, status := f.root.Lookup(context.Background(), "cafe\u0301", &out); status != syscall.ENOTSUP {
		t.Fatalf("ambiguous lookup: %v", status)
	}
}

func TestRenameStalesOverwrittenBindings(t *testing.T) {
	f := newTestFrontend(t, false, decomposedName)
	f.put(t, "source", "source")
	f.put(t, "target", "target")
	root := f.rootNode()
	source := f.lookup(t, root, "source")
	target := f.lookup(t, root, "target")
	// Populate only the old destination's content cache. The source has
	// not been read, and equal size/second-resolution mtimes are common.
	expectRead(t, target.n, "target")
	f.rename(t, root, "source", root, "target")
	expectRead(t, source.n, "source")
	if got, status := target.n.Write(context.Background(), nil, []byte("bad"), 0); status != syscall.ESTALE {
		t.Fatalf("overwritten node write=%d %v", got, status)
	}
	if status := target.n.Fsync(context.Background(), nil, 0); status != syscall.ESTALE {
		t.Fatalf("overwritten node fsync=%v", status)
	}
	next := f.lookup(t, root, "target")
	if next.id != source.id {
		t.Fatal("rename changed the source inode identity")
	}
	f.readRemote(t, "target", "source")
}

func TestUnregisteredLookupCandidateSurvivesRename(t *testing.T) {
	f := newTestFrontend(t, false, decomposedName)
	f.put(t, "café", "data")
	inode, status := f.root.Lookup(context.Background(), "cafe\u0301", &fuse.EntryOut{})
	if status != 0 {
		t.Fatal(status)
	}
	candidate := inode.Operations().(*node)
	// No go-fuse tree registration has happened for this callback result.
	if len(f.root.Children()) != 0 {
		t.Fatal("candidate was unexpectedly registered")
	}
	if status := f.root.Rename(context.Background(), "cafe\u0301", f.root, "moved", 0); status != 0 {
		t.Fatal(status)
	}
	expectRead(t, candidate, "data")
}

func TestLiteralNamespaceKeepsDifferentUnicodeNames(t *testing.T) {
	f := newTestFrontend(t, false, nil)
	f.put(t, "café", "one")
	f.put(t, "cafe\u0301", "two")
	root := f.rootNode()
	first := f.lookup(t, root, "café")
	second := f.lookup(t, root, "cafe\u0301")
	if first.id == second.id {
		t.Fatal("literal namespace merged distinct names")
	}
	expectRead(t, first.n, "one")
	expectRead(t, second.n, "two")
}

func TestLiteralNamespaceCanRenameBetweenUnicodeSpellings(t *testing.T) {
	f := newTestFrontend(t, false, nil)
	f.put(t, "café", "data")
	root := f.rootNode()
	old := f.lookup(t, root, "café")
	f.rename(t, root, "café", root, "cafe\u0301")
	if raw, _ := old.n.object.current(); raw != "cafe\u0301" {
		t.Fatalf("literal rename raw path=%q", raw)
	}
	expectRead(t, old.n, "data")
	f.readRemote(t, "cafe\u0301", "data")
	if _, err := f.remote.ReadFile(context.Background(), "café", 1<<20); !modalapi.IsNotFound(err) {
		t.Fatalf("literal source remained: %v", err)
	}
}

func TestBindingRenameGuardsOnlyAffectedObjects(t *testing.T) {
	ids := newBindings()
	ids.names.Lock()
	defer ids.names.Unlock()
	source := ids.get("source", syscall.S_IFREG)
	target := ids.get("target", syscall.S_IFREG)
	other := ids.get("other", syscall.S_IFREG)
	finish := ids.renameGuard("source", "target")
	var once sync.Once
	done := func() { once.Do(func() { finish(nil) }) }
	defer done()
	unrelated := make(chan syscall.Errno, 1)
	go func() {
		_, release, status := other.acquire()
		if release != nil {
			release()
		}
		unrelated <- status
	}()
	select {
	case status := <-unrelated:
		if status != 0 {
			t.Fatal(status)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated I/O blocked on rename")
	}
	waiting := make(chan string, 1)
	go func() {
		p, release, status := source.acquire()
		if release != nil {
			release()
		}
		waiting <- fmt.Sprintf("%s:%d", p, status)
	}()
	select {
	case <-waiting:
		t.Fatal("source I/O escaped rename guard")
	case <-time.After(20 * time.Millisecond):
	}
	done()
	select {
	case got := <-waiting:
		if got != "target:0" {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("source I/O didn't resume")
	}
	if _, release, status := target.acquire(); status != syscall.ESTALE {
		if release != nil {
			release()
		}
		t.Fatalf("destination status=%v", status)
	}
}

func TestBindingCacheDoesNotRetainForgottenIdentities(t *testing.T) {
	ids := newBindings()
	ids.names.Lock()
	defer ids.names.Unlock()
	for i := 0; i < 256; i++ {
		ids.get(fmt.Sprintf("entry-%d", i), syscall.S_IFREG)
	}
	runtime.GC()
	live := ids.get("live", syscall.S_IFREG)
	ids.mu.Lock()
	count := len(ids.byPath)
	ids.mu.Unlock()
	runtime.KeepAlive(live)
	if count > 2 {
		t.Fatalf("weak index retained %d forgotten identities", count)
	}
}

func TestBindingCacheSweepsBelowOldHighWaterMark(t *testing.T) {
	ids := newBindings()
	ids.names.Lock()
	defer ids.names.Unlock()
	live := ids.get("live", syscall.S_IFREG)
	for i := 0; i < 1000; i++ {
		ids.get(fmt.Sprintf("forgotten-%d", i), syscall.S_IFREG)
	}
	runtime.GC()
	ids.mu.Lock()
	ids.swept = time.Now().Add(-2 * time.Second)
	ids.mu.Unlock()
	if got := ids.get("live", syscall.S_IFREG); got != live {
		t.Fatal("cache sweep replaced a live identity")
	}
	ids.mu.Lock()
	count := len(ids.byPath)
	ids.mu.Unlock()
	runtime.KeepAlive(live)
	if count != 1 {
		t.Fatalf("old high-water mark retained %d bindings", count)
	}
}

type heldCopy struct {
	pb.ModalClientClient
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *heldCopy) VolumeCopyFiles2(ctx context.Context, req *pb.VolumeCopyFiles2Request, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	c.once.Do(func() { close(c.entered) })
	<-c.release
	return c.ModalClientClient.VolumeCopyFiles2(ctx, req, opts...)
}

func TestSlowRenameKeepsUnrelatedRawIOAvailable(t *testing.T) {
	f := newTestFrontend(t, false, decomposedName)
	f.put(t, "source", "source")
	f.put(t, "other", "other")
	root := f.rootNode()
	source := f.lookup(t, root, "source")
	other := f.lookup(t, root, "other")
	gate := &heldCopy{ModalClientClient: f.client.Stub, entered: make(chan struct{}), release: make(chan struct{})}
	f.client.Stub = gate
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	defer release()
	done := make(chan syscall.Errno, 1)
	go func() { done <- f.root.Rename(context.Background(), "source", f.root, "moved", 0) }()
	select {
	case <-gate.entered:
	case <-time.After(time.Second):
		t.Fatal("rename didn't reach held copy")
	}
	read := make(chan syscall.Errno, 1)
	go func() {
		got, status := readNode(other.n)
		if status == 0 && got != "other" {
			status = syscall.EIO
		}
		read <- status
	}()
	select {
	case status := <-read:
		if status != 0 {
			t.Fatal(status)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated raw read blocked behind rename")
	}
	if status := other.n.Fsync(context.Background(), nil, 0); status != 0 {
		t.Fatal(status)
	}
	created := make(chan syscall.Errno, 1)
	go func() {
		_, _, _, status := f.root.Create(context.Background(), "created", syscall.O_CREAT|syscall.O_EXCL|syscall.O_RDWR, 0o644, &fuse.EntryOut{})
		created <- status
	}()
	select {
	case status := <-created:
		t.Fatalf("create escaped held namespace rename: %v", status)
	case <-time.After(20 * time.Millisecond):
	}
	release()
	select {
	case status := <-done:
		if status != 0 {
			t.Fatal(status)
		}
	case <-time.After(time.Second):
		t.Fatal("rename didn't finish")
	}
	select {
	case status := <-created:
		if status != 0 {
			t.Fatal(status)
		}
	case <-time.After(time.Second):
		t.Fatal("create deadlocked with rename guard")
	}
	expectRead(t, source.n, "source")
}

func TestConcurrentParentAndChildRenamesKeepBindings(t *testing.T) {
	for i := 0; i < 12; i++ {
		f := newTestFrontend(t, false, decomposedName)
		f.put(t, "left/café", "data")
		root := f.rootNode()
		left := f.lookup(t, root, "left")
		leaf := f.lookup(t, left, "cafe\u0301")
		start := make(chan struct{})
		done := make(chan syscall.Errno, 2)
		go func() { <-start; done <- f.root.Rename(context.Background(), "left", f.root, "moved", 0) }()
		go func() { <-start; done <- left.n.Rename(context.Background(), "cafe\u0301", left.n, "renamed", 0) }()
		close(start)
		for range 2 {
			select {
			case status := <-done:
				if status != 0 {
					t.Fatal(status)
				}
			case <-time.After(time.Second):
				t.Fatal("overlapping renames deadlocked")
			}
		}
		if raw, status := leaf.n.object.current(); status != 0 || raw != "moved/renamed" {
			t.Fatalf("descendant bound to %q: %v", raw, status)
		}
		expectRead(t, leaf.n, "data")
		f.readRemote(t, "moved/renamed", "data")
	}
}

func TestConcurrentCrossDirectoryRenamesKeepBindings(t *testing.T) {
	for i := 0; i < 12; i++ {
		f := newTestFrontend(t, false, decomposedName)
		f.put(t, "left/a", "first")
		f.put(t, "right/b", "second")
		// Empty directories are materialized with the same marker used by
		// the mount, so moving the only file doesn't remove its parent.
		f.put(t, "left/"+vfs.KeepName, "")
		f.put(t, "right/"+vfs.KeepName, "")
		root := f.rootNode()
		left := f.lookup(t, root, "left")
		right := f.lookup(t, root, "right")
		first := f.lookup(t, left, "a")
		second := f.lookup(t, right, "b")
		start := make(chan struct{})
		done := make(chan syscall.Errno, 2)
		go func() { <-start; done <- left.n.Rename(context.Background(), "a", right.n, "a", 0) }()
		go func() { <-start; done <- right.n.Rename(context.Background(), "b", left.n, "b", 0) }()
		close(start)
		for range 2 {
			select {
			case status := <-done:
				if status != 0 {
					t.Fatal(status)
				}
			case <-time.After(time.Second):
				t.Fatal("cross-directory renames deadlocked")
			}
		}
		if raw, _ := first.n.object.current(); raw != "right/a" {
			t.Fatal(raw)
		}
		if raw, _ := second.n.object.current(); raw != "left/b" {
			t.Fatal(raw)
		}
		expectRead(t, first.n, "first")
		expectRead(t, second.n, "second")
	}
}

func TestFailedRenameKeepsBoundObjectsUsable(t *testing.T) {
	f := newTestFrontend(t, false, decomposedName)
	f.put(t, "source/file", "source")
	f.put(t, "target/file", "target")
	root := f.rootNode()
	source := f.lookup(t, root, "source")
	target := f.lookup(t, root, "target")
	first := f.lookup(t, source, "file")
	second := f.lookup(t, target, "file")
	if status := f.root.Rename(context.Background(), "source", f.root, "target", 0); status == 0 {
		t.Fatal("rename onto nonempty directory succeeded")
	}
	expectRead(t, first.n, "source")
	expectRead(t, second.n, "target")
}

func TestBoundRawIOIgnoresClientKeyAvailability(t *testing.T) {
	var unavailable atomic.Bool
	var failedCalls atomic.Int64
	key := func(name string) (string, error) {
		if unavailable.Load() {
			failedCalls.Add(1)
			return "", syscall.EIO
		}
		return decomposedName(name)
	}
	f := newTestFrontend(t, false, key)
	f.put(t, "café.txt", "remote")
	remote := f.lookup(t, f.rootNode(), "cafe\u0301.txt")
	created, _, _, status := f.root.Create(context.Background(), "local.txt", syscall.O_CREAT|syscall.O_EXCL|syscall.O_RDWR, 0o644, &fuse.EntryOut{})
	if status != 0 {
		t.Fatal(status)
	}
	local := created.Operations().(*node)
	if _, status := local.Write(context.Background(), nil, []byte("pending"), 0); status != 0 {
		t.Fatal(status)
	}
	unavailable.Store(true)
	defer unavailable.Store(false)
	time.Sleep(3 * time.Millisecond)
	if status := remote.n.Getattr(context.Background(), nil, &fuse.AttrOut{}); status != 0 {
		t.Fatal(status)
	}
	expectRead(t, remote.n, "remote")
	if status := local.Fsync(context.Background(), nil, 0); status != 0 {
		t.Fatal(status)
	}
	if err := f.v.Flush("local.txt"); err != nil {
		t.Fatal(err)
	}
	if count := failedCalls.Load(); count != 0 {
		t.Fatalf("bound raw IO called client key %d times", count)
	}
	if _, status := f.root.Lookup(context.Background(), "new.txt", &fuse.EntryOut{}); status != syscall.EIO {
		t.Fatalf("new client lookup must report converter failure: %v", status)
	}
}

type failSourceRemoval struct{ pb.ModalClientClient }

func (c failSourceRemoval) VolumeRemoveFile2(ctx context.Context, req *pb.VolumeRemoveFile2Request, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	if req.GetPath() == "source" {
		return nil, status.Error(codes.PermissionDenied, "injected source removal failure")
	}
	return c.ModalClientClient.VolumeRemoveFile2(ctx, req, opts...)
}

func TestPartialRenameStalesReplacedDestination(t *testing.T) {
	f := newTestFrontend(t, false, decomposedName)
	f.put(t, "source", "source")
	f.put(t, "target", "target")
	root := f.rootNode()
	source := f.lookup(t, root, "source")
	target := f.lookup(t, root, "target")
	expectRead(t, target.n, "target")
	f.client.Stub = failSourceRemoval{f.client.Stub}
	if code := f.root.Rename(context.Background(), "source", f.root, "target", 0); code == 0 {
		t.Fatal("injected removal failure was ignored")
	}
	f.readRemote(t, "target", "source")
	f.readRemote(t, "source", "source")
	expectRead(t, source.n, "source")
	next := f.lookup(t, root, "target")
	expectRead(t, next.n, "source")
	if _, code := target.n.Write(context.Background(), nil, []byte("old edit"), 0); code != syscall.ESTALE {
		t.Fatalf("old destination handle remained writable after partial replacement: %v", code)
	}
}

func TestAdvertisedClientNameBoundAndUnaddressableListing(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		t.Run(fmt.Sprintf("encrypted=%v", encrypted), func(t *testing.T) {
			f := newTestFrontend(t, encrypted, decomposedName)
			// 139 raw UTF-8 bytes fit the encrypted format too. Its decomposed
			// client spelling is 274 bytes, beyond this frontend's supported bound.
			original := strings.Repeat("가", 45) + ".bin"
			client := norm.NFD.String(original)
			f.put(t, original, "original")
			f.put(t, "notes", "notes")
			var stat fuse.StatfsOut
			if code := f.root.Statfs(context.Background(), &stat); code != 0 || stat.NameLen != maxClientNameBytes {
				t.Fatalf("advertised name bound=%d %v", stat.NameLen, code)
			}
			var out fuse.EntryOut
			if code := f.wire.Lookup(nil, &fuse.InHeader{NodeId: 1}, client, &out); syscall.Errno(code) != syscall.ENAMETOOLONG {
				t.Fatalf("overlong lookup=%v", code)
			}
			if _, _, _, code := f.root.Create(context.Background(), client, syscall.O_CREAT|syscall.O_EXCL|syscall.O_RDWR, 0o644, &out); code != syscall.ENAMETOOLONG {
				t.Fatal(code)
			}
			if _, code := f.root.Mkdir(context.Background(), client, 0o755, &out); code != syscall.ENAMETOOLONG {
				t.Fatal(code)
			}
			if code := f.root.Unlink(context.Background(), client); code != syscall.ENAMETOOLONG {
				t.Fatal(code)
			}
			if code := f.root.Rename(context.Background(), "notes", f.root, client, 0); code != syscall.ENAMETOOLONG {
				t.Fatal(code)
			}
			stream, code := f.root.Readdir(context.Background())
			if code != 0 {
				t.Fatal(code)
			}
			defer stream.Close()
			var names []string
			for stream.HasNext() {
				entry, code := stream.Next()
				if code != 0 {
					t.Fatal(code)
				}
				names = append(names, entry.Name)
			}
			if len(names) != 1 || names[0] != "notes" {
				t.Fatalf("visible names=%q", names)
			}
			reported := false
			for _, event := range f.events() {
				reported = reported || strings.HasPrefix(event, "unreadable:") && strings.Contains(event, "255")
			}
			if !reported {
				t.Fatalf("unrepresentable name was omitted without Activity explanation: %q", f.events())
			}
			f.readRemote(t, original, "original")
			f.readRemote(t, "notes", "notes")
		})
	}
}

type lostRemoveResponse struct {
	pb.ModalClientClient
	inject   atomic.Bool
	once     sync.Once
	applyErr error
}

func (c *lostRemoveResponse) VolumeRemoveFile2(ctx context.Context, req *pb.VolumeRemoveFile2Request, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	if req.GetPath() == "victim" && c.inject.Load() {
		c.once.Do(func() { _, c.applyErr = c.ModalClientClient.VolumeRemoveFile2(ctx, req, opts...) })
		if c.applyErr != nil {
			return nil, c.applyErr
		}
		return nil, status.Error(codes.Unavailable, "injected lost applied-remove response")
	}
	return c.ModalClientClient.VolumeRemoveFile2(ctx, req, opts...)
}

func TestAppliedRemoveErrorNeverRevivesOldBinding(t *testing.T) {
	f := newTestFrontend(t, false, decomposedName)
	f.put(t, "victim", "original")
	old := f.lookup(t, f.rootNode(), "victim")
	lost := &lostRemoveResponse{ModalClientClient: f.client.Stub}
	lost.inject.Store(true)
	f.client.Stub = lost
	if code := f.root.Unlink(context.Background(), "victim"); code == 0 {
		t.Fatal("injected lost removal response was ignored")
	}
	lost.inject.Store(false)
	if _, err := f.remote.ReadFile(context.Background(), "victim", 1<<20); !modalapi.IsNotFound(err) {
		t.Fatalf("fixture removal wasn't applied: %v", err)
	}
	if code := f.root.Unlink(context.Background(), "victim"); code != syscall.ENOENT {
		t.Fatalf("retry should see removed name: %v", code)
	}
	created, _, _, code := f.root.Create(context.Background(), "victim", syscall.O_CREAT|syscall.O_EXCL|syscall.O_RDWR, 0o644, &fuse.EntryOut{})
	if code != 0 {
		t.Fatal(code)
	}
	next := created.Operations().(*node)
	if next.object.ino == old.n.object.ino {
		t.Fatal("new create revived an invalidated inode identity")
	}
	if _, code := next.Write(context.Background(), nil, []byte("successor"), 0); code != 0 {
		t.Fatal(code)
	}
	if err := f.v.Flush("victim"); err != nil {
		t.Fatal(err)
	}
	if _, code := old.n.Write(context.Background(), nil, []byte("old edit"), 0); code != syscall.ESTALE {
		t.Fatalf("removed old binding wrote into successor: %v", code)
	}
	f.readRemote(t, "victim", "successor")
}
