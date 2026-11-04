package fusefront

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/hanwen/go-fuse/v2/fuse"
	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestNewCreateRetiresRemoteRemovedBinding(t *testing.T) {
	f := newTestFrontend(t, false, decomposedName)
	f.put(t, "victim", "original")
	old := f.lookup(t, f.rootNode(), "victim")
	if err := f.remote.Remove(context.Background(), "victim", false); err != nil {
		t.Fatal(err)
	}
	created, _, _, code := f.root.Create(context.Background(), "victim", syscall.O_CREAT|syscall.O_EXCL|syscall.O_RDWR, 0o644, &fuse.EntryOut{})
	if code != 0 {
		t.Fatal(code)
	}
	next := created.Operations().(*node)
	if next.object.ino == old.n.object.ino {
		t.Fatal("new exclusive creation reused an old inode")
	}
	if _, code := next.Write(context.Background(), nil, []byte("successor"), 0); code != 0 {
		t.Fatal(code)
	}
	if err := f.v.Flush("victim"); err != nil {
		t.Fatal(err)
	}
	if _, code := old.n.Write(context.Background(), nil, []byte("old edit"), 0); code != syscall.ESTALE {
		t.Fatalf("old fd can edit new exclusive creation: %v", code)
	}
	f.readRemote(t, "victim", "successor")
}

func TestNewMkdirRetiresOldDirectoryAndDescendants(t *testing.T) {
	f := newTestFrontend(t, false, decomposedName)
	f.put(t, "dir/child", "original")
	old := f.lookup(t, f.rootNode(), "dir")
	child := f.lookup(t, old, "child")
	if err := f.remote.Remove(context.Background(), "dir", true); err != nil {
		t.Fatal(err)
	}
	created, code := f.root.Mkdir(context.Background(), "dir", 0o755, &fuse.EntryOut{})
	if code != 0 {
		t.Fatal(code)
	}
	next := created.Operations().(*node)
	if next.object.ino == old.n.object.ino {
		t.Fatal("new directory reused old identity")
	}
	if _, _, _, code := old.n.Create(context.Background(), "bad", syscall.O_CREAT|syscall.O_EXCL|syscall.O_RDWR, 0o644, &fuse.EntryOut{}); code != syscall.ESTALE {
		t.Fatalf("old directory can modify recreated directory: %v", code)
	}
	if _, code := child.n.Write(context.Background(), nil, []byte("bad"), 0); code != syscall.ESTALE {
		t.Fatalf("old descendant remained writable: %v", code)
	}
}

func TestOldRawWriteCannotRecreateDeletedBase(t *testing.T) {
	f := newTestFrontend(t, false, decomposedName)
	f.put(t, "file", "original")
	old := f.lookup(t, f.rootNode(), "file")
	if err := f.remote.Remove(context.Background(), "file", false); err != nil {
		t.Fatal(err)
	}
	if _, code := old.n.Write(context.Background(), nil, []byte("tail"), 4); code != syscall.ENOENT {
		t.Fatalf("write to missing base=%v", code)
	}
	if count := f.v.Stats()["open_files"].(int); count != 0 {
		t.Fatalf("missing-base write created %d journals", count)
	}
}

func TestCreateGuardRevalidatesAfterWaitingForOldWriter(t *testing.T) {
	f := newTestFrontend(t, false, decomposedName)
	f.put(t, "victim", "original")
	old := f.lookup(t, f.rootNode(), "victim")
	if err := f.remote.Remove(context.Background(), "victim", false); err != nil {
		t.Fatal(err)
	}
	_, release, code := old.n.object.acquire()
	if code != 0 {
		t.Fatal(code)
	}
	var releaseOnce sync.Once
	unlock := func() { releaseOnce.Do(release) }
	defer unlock()
	entered := make(chan struct{})
	done := make(chan error, 1)
	var enterOnce sync.Once
	go func() {
		f.root.ids.names.Lock()
		defer f.root.ids.names.Unlock()
		_, _, err := f.v.Names().CreateGuarded("", "victim", true, nil, func(raw string, exists bool) func(error) {
			enterOnce.Do(func() { close(entered) })
			return f.root.ids.createGuard(raw, exists)
		})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("create guard wasn't entered")
	}
	// The raw API deliberately reserves a competing local state, simulating an
	// in-flight writer that publishes while the namespace waits for its binding.
	if _, err := f.v.WriteAt("victim", []byte("old writer"), 0); err != nil {
		t.Fatal(err)
	}
	unlock()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("exclusive create ignored the competing reservation")
		}
	case <-time.After(time.Second):
		t.Fatal("create retry retained its previous binding lock")
	}
	expectRead(t, old.n, "old writer")
}

func TestExplicitCGJAndLongNonstarterNamesRemainSeparate(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		t.Run(fmt.Sprintf("encrypted=%v", encrypted), func(t *testing.T) {
			// These particular inputs already have their exact native byte spelling.
			// An accurate key must preserve both, rather than inserting a CGJ after 30 marks.
			key := func(s string) (string, error) { return s, nil }
			f := newTestFrontend(t, encrypted, key)
			without := "a" + strings.Repeat("\u0301", 31)
			explicit := "a" + strings.Repeat("\u0301", 30) + "\u034f\u0301"
			f.put(t, explicit, "explicit preserved")
			old := f.lookup(t, f.rootNode(), explicit)
			created, _, _, code := f.root.Create(context.Background(), without, syscall.O_CREAT|syscall.O_RDWR|syscall.O_TRUNC, 0o644, &fuse.EntryOut{})
			if code != 0 {
				t.Fatal(code)
			}
			next := created.Operations().(*node)
			if next.object.ino == old.n.object.ino {
				t.Fatal("distinct native filenames share identity")
			}
			if _, code := next.Write(context.Background(), nil, []byte("new distinct"), 0); code != 0 {
				t.Fatal(code)
			}
			if err := f.v.Flush(without); err != nil {
				t.Fatal(err)
			}
			f.readRemote(t, explicit, "explicit preserved")
			f.readRemote(t, without, "new distinct")
			expectRead(t, old.n, "explicit preserved")
		})
	}
}

type lostMkdirResponse struct {
	pb.ModalClientClient
	once     sync.Once
	inject   bool
	response *pb.VolumePutFiles2Response
	applyErr error
}

func (c *lostMkdirResponse) VolumePutFiles2(ctx context.Context, req *pb.VolumePutFiles2Request, opts ...grpc.CallOption) (*pb.VolumePutFiles2Response, error) {
	if c.inject && len(req.GetFiles()) == 1 && req.GetFiles()[0].GetPath() == "dir/.modalfs_keep" {
		c.once.Do(func() { c.response, c.applyErr = c.ModalClientClient.VolumePutFiles2(ctx, req, opts...) })
		if c.applyErr != nil {
			return nil, c.applyErr
		}
		return nil, status.Error(codes.Unavailable, "injected lost mkdir response")
	}
	return c.ModalClientClient.VolumePutFiles2(ctx, req, opts...)
}
func TestAppliedMkdirErrorRetiresOldDirectory(t *testing.T) {
	f := newTestFrontend(t, false, decomposedName)
	f.put(t, "dir/child", "original")
	old := f.lookup(t, f.rootNode(), "dir")
	child := f.lookup(t, old, "child")
	if err := f.remote.Remove(context.Background(), "dir", true); err != nil {
		t.Fatal(err)
	}
	lost := &lostMkdirResponse{ModalClientClient: f.client.Stub, inject: true}
	f.client.Stub = lost
	if _, code := f.root.Mkdir(context.Background(), "dir", 0o755, &fuse.EntryOut{}); code == 0 {
		t.Fatal("lost mkdir response wasn't reported")
	}
	lost.inject = false
	if _, code := f.root.Mkdir(context.Background(), "dir", 0o755, &fuse.EntryOut{}); code != syscall.EEXIST {
		t.Fatalf("retry should find published directory: %v", code)
	}
	if _, code := old.n.Lookup(context.Background(), "child", &fuse.EntryOut{}); code != syscall.ESTALE {
		t.Fatalf("old directory remained active after attempted new creation: %v", code)
	}
	if _, code := child.n.Write(context.Background(), nil, []byte("bad"), 0); code != syscall.ESTALE {
		t.Fatalf("old child remained active: %v", code)
	}
}

func TestUnsupportedEncryptedNameKeepsUnrelatedFilesAvailable(t *testing.T) {
	key := func(s string) (string, error) {
		if !utf8.ValidString(s) {
			return "", syscall.EINVAL
		}
		return decomposedName(s)
	}
	f := newTestFrontend(t, true, key)
	f.put(t, "bad"+string([]byte{0xff}), "legacy encrypted bytes")
	f.put(t, "notes", "notes")
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
		t.Fatalf("unsupported entry hid valid neighbors: %q", names)
	}
	notes := f.lookup(t, f.rootNode(), "notes")
	expectRead(t, notes.n, "notes")
	reported := false
	for _, event := range f.events() {
		reported = reported || strings.HasPrefix(event, "unreadable:") && strings.Contains(event, "Linux")
	}
	if !reported {
		t.Fatalf("missing encryption-aware recovery action: %q", f.events())
	}
}
