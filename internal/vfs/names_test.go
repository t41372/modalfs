package vfs

import (
	"context"
	"errors"
	"fmt"
	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"golang.org/x/text/unicode/norm"
	"google.golang.org/grpc"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func testNameKey(name string) (string, error) { return norm.NFD.String(name), nil }

func TestNamespaceResolvesNames(t *testing.T) {
	f := newFakeFS(t, Options{WriteBack: time.Hour, NameKey: testNameKey})
	f.put(t, "café/â\u0084¢.rar", []byte("remote"))
	ns := f.Names()
	parent, _, err := ns.Lookup("", "cafe\u0301")
	if err != nil {
		t.Fatal(err)
	}
	for _, spelling := range []string{"â\u0084¢.rar", norm.NFD.String("â\u0084¢.rar")} {
		p, _, err := ns.Lookup(parent, spelling)
		if err != nil || p != "café/â\u0084¢.rar" {
			t.Fatalf("resolve %q: %q %v", spelling, p, err)
		}
	}
	p, _, err := ns.Create(parent, "résumé", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := ns.Lookup(parent, norm.NFD.String("résumé"))
	if err != nil || got != p {
		t.Fatalf("local name: %q %v", got, err)
	}
}

func TestNamespaceRefusesAmbiguityButListsOtherFiles(t *testing.T) {
	for _, pair := range [][2]string{{"café", "cafe\u0301"}, {"Å", "A\u030a"}, {"ά", "ά"}} {
		t.Run(pair[0], func(t *testing.T) {
			f := newFakeFS(t, Options{NameKey: testNameKey})
			f.put(t, pair[0], []byte("first"))
			f.put(t, pair[1], []byte("second"))
			f.put(t, "notes.txt", []byte("unaffected"))
			for _, name := range pair {
				if _, _, err := f.Names().Lookup("", name); !errors.Is(err, syscall.ENOTSUP) {
					t.Fatalf("ambiguous %q: %v", name, err)
				}
			}
			entries, err := f.Names().ReadDir("")
			if err != nil || len(entries) != 1 || entries[0].Name != "notes.txt" {
				t.Fatalf("listing: %+v %v", entries, err)
			}
		})
	}
}

func TestLiteralNamespaceKeepsBothUnicodeSpellings(t *testing.T) {
	f := newFakeFS(t, Options{})
	for _, name := range []string{"café", "cafe\u0301"} {
		p, _, err := f.Names().Create("", name, true, nil)
		if err != nil || p != name {
			t.Fatalf("literal %q: %q %v", name, p, err)
		}
	}
	if _, _, err := f.Names().Create("", "café", true, nil); !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
}

func TestNamespaceConcurrentEquivalentCreateHasOneWinner(t *testing.T) {
	f := newFakeFS(t, Options{NameKey: testNameKey, WriteBack: time.Hour})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	start := make(chan struct{})
	for _, name := range []string{"café", "cafe\u0301"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			<-start
			_, _, err := f.Names().Create("", name, true, nil)
			errs <- err
		}(name)
	}
	close(start)
	wg.Wait()
	close(errs)
	successes, exists := 0, 0
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, os.ErrExist) {
			exists++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || exists != 1 {
		t.Fatalf("successes=%d exists=%d", successes, exists)
	}
}

func TestNamespaceCreatePreservesItsSelectedBase(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	key := func(name string) (string, error) {
		if name == "café" && once.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
		return testNameKey(name)
	}
	f := newFakeFS(t, Options{NameKey: key, WriteBack: time.Hour})
	f.put(t, "café", []byte("old"))
	done := make(chan error, 1)
	go func() { size := int64(0); _, _, err := f.Names().Create("", "cafe\u0301", false, &size); done <- err }()
	<-entered
	f.put(t, "café", []byte("new remote version"))
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt("café", []byte("local"), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Flush("café"); err != nil {
		t.Fatal(err)
	}
	got, err := f.client.ReadFile(context.Background(), f.vol.ID, "café", 1024)
	if err != nil || string(got) != "new remote version" {
		t.Fatalf("remote version overwritten: %q %v", got, err)
	}
	entries, err := f.client.List(context.Background(), f.vol.ID, "", false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if strings.Contains(entry.Path, "conflict") {
			data, err := f.client.ReadFile(context.Background(), f.vol.ID, entry.Path, 1024)
			if err != nil {
				t.Fatal(err)
			}
			found = string(data) == "local"
		}
	}
	if !found {
		t.Fatalf("local version missing: %+v", entries)
	}
}

func TestNameKeyCanReenterReadOnlyFSMethods(t *testing.T) {
	var f *fakeFS
	key := func(name string) (string, error) {
		if f != nil {
			_ = f.Stats()
		}
		return testNameKey(name)
	}
	f = newFakeFS(t, Options{NameKey: key, WriteBack: time.Hour, FilterJunk: true})
	f.put(t, "café", []byte("remote"))
	if _, _, err := f.Names().Lookup("", "cafe\u0301"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.Names().Create("", "pending", true, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.ephCreate(".DS_Store", true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Names().ReadDir(""); err != nil {
		t.Fatal(err)
	}
}

type concurrentNameLists struct {
	pb.ModalClientClient
	count    atomic.Int64
	expected int64
	release  chan struct{}
}

func (g *concurrentNameLists) VolumeListFiles2(ctx context.Context, req *pb.VolumeListFiles2Request, opts ...grpc.CallOption) (grpc.ServerStreamingClient[pb.VolumeListFiles2Response], error) {
	number := g.count.Add(1)
	if number <= g.expected {
		if number == g.expected {
			close(g.release)
		}
		<-g.release
	}
	return g.ModalClientClient.VolumeListFiles2(ctx, req, opts...)
}

func TestConcurrentUnrelatedNameSnapshotsDoNotRetry(t *testing.T) {
	const count = 24
	f := newFakeFS(t, Options{NameKey: testNameKey, WriteBack: time.Hour, DirCacheTTL: time.Hour})
	if _, err := f.listDir("", false); err != nil {
		t.Fatal(err)
	}
	gate := &concurrentNameLists{ModalClientClient: f.client.Stub, expected: count, release: make(chan struct{})}
	f.client.Stub = gate
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); errs <- f.Create(fmt.Sprintf("pending-%02d", i), true) }(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := gate.count.Load(); got != count {
		t.Fatalf("unrelated activity caused %d listing RPCs, want %d", got, count)
	}
	f.mu.Lock()
	remaining := len(f.nameWatches)
	f.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("%d snapshot watches leaked", remaining)
	}
}

func TestSameNameChangeInvalidatesOnlyItsSnapshot(t *testing.T) {
	f := newFakeFS(t, Options{NameKey: testNameKey, WriteBack: time.Hour})
	key, _ := testNameKey("café")
	snapshot, err := f.freshNames("", key)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Create("café", true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.stateFromNameSnapshot("cafe\u0301", true, key, snapshot); !errors.Is(err, errNameChanged) {
		t.Fatalf("same key change was ignored: %v", err)
	}
	if _, _, err := f.Names().Create("", "cafe\u0301", true, nil); !errors.Is(err, os.ErrExist) {
		t.Fatalf("canonical duplicate: %v", err)
	}
	f.mu.Lock()
	remaining := len(f.nameWatches)
	f.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("%d snapshot watches leaked", remaining)
	}
}

func TestUnrelatedRefreshDoesNotInvalidateNameSnapshot(t *testing.T) {
	f := newFakeFS(t, Options{NameKey: testNameKey, WriteBack: time.Hour})
	snapshot, err := f.freshNames("", "target")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Create("unrelated", true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.listDir("", true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.stateFromNameSnapshot("target", true, "target", snapshot); err != nil {
		t.Fatalf("unrelated refresh invalidated selection: %v", err)
	}
}

func TestFrontendNameLimitDoesNotHideOtherFiles(t *testing.T) {
	var events []string
	f := newFakeFS(t, Options{NameKey: testNameKey, OnEvent: func(kind, p, detail string) { events = append(events, kind) }})
	long := strings.Repeat("é", 127)
	f.put(t, long, []byte("kept"))
	f.put(t, "notes", []byte("visible"))
	all, err := f.Names().ReadDir("")
	if err != nil || len(all) != 2 {
		t.Fatalf("unlimited view: %+v %v", all, err)
	}
	limited, err := f.Names().ReadDirLimit("", 255)
	if err != nil || len(limited) != 1 || limited[0].Name != "notes" {
		t.Fatalf("NFS view: %+v %v", limited, err)
	}
	if len(events) != 1 || events[0] != "unreadable" {
		t.Fatalf("excluded filename not reported truthfully: %v", events)
	}
	data, err := f.client.ReadFile(context.Background(), f.vol.ID, long, 1024)
	if err != nil || string(data) != "kept" {
		t.Fatalf("long file changed: %q %v", data, err)
	}
}

func TestInvalidNewPathsAreRejectedBeforeJournalCreation(t *testing.T) {
	for _, name := range []string{"bad\xff", "bad\x00name"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			f := newFakeFS(t, Options{WriteBack: time.Hour})
			if err := f.Create(name, true); !errors.Is(err, syscall.EINVAL) {
				t.Fatalf("raw create accepted invalid path: %v", err)
			}
			if _, err := f.WriteAt(name, []byte("must not be accepted"), 0); !errors.Is(err, syscall.EINVAL) {
				t.Fatalf("raw write accepted invalid path: %v", err)
			}
			if _, _, err := f.Names().Create("", name, true, nil); !errors.Is(err, syscall.EINVAL) {
				t.Fatalf("namespace create accepted invalid path: %v", err)
			}
			if f.Stats()["open_files"] != 0 {
				t.Fatal("invalid path created file state")
			}
			pending := f.PendingFiles()
			if len(pending) != 0 {
				t.Fatalf("invalid path journaled: %+v", pending)
			}
		})
	}
}

func TestUnsupportedNameDoesNotPoisonCachedNeighbor(t *testing.T) {
	var events []string
	key := func(name string) (string, error) {
		if name == "bad" {
			return "", syscall.EINVAL
		}
		return name, nil
	}
	f := newFakeFS(t, Options{NameKey: key, OnEvent: func(kind, p, detail string) { events = append(events, p) }})
	f.put(t, "bad", []byte("kept"))
	f.put(t, "notes", []byte("visible"))
	entries, err := f.Names().ReadDirLimit("", 255)
	if err != nil || len(entries) != 1 || entries[0].Name != "notes" {
		t.Fatalf("unsupported name poisoned valid neighbor: %+v %v", entries, err)
	}
	if len(events) != 1 || events[0] != "bad" {
		t.Fatalf("wrong exclusions reported: %v", events)
	}
}

func TestReservedNamesNeverCreateStateOrShadows(t *testing.T) {
	f := newFakeFS(t, Options{NameKey: testNameKey, WriteBack: time.Hour})
	f.put(t, ".modalfs/owned", []byte("metadata"))
	f.put(t, "dir/.modalfs_keep", nil)
	for _, target := range []struct{ parent, name string }{{"", ".modalfs"}, {"dir", KeepName}} {
		if _, _, err := f.Names().Create(target.parent, target.name, true, nil); !errors.Is(err, syscall.EACCES) {
			t.Fatalf("reserved create: %v", err)
		}
		if _, _, err := f.Names().Mkdir(target.parent, target.name); !errors.Is(err, syscall.EACCES) {
			t.Fatalf("reserved directory: %v", err)
		}
		raw := target.name
		if target.parent != "" {
			raw = target.parent + "/" + target.name
		}
		if _, err := f.WriteAt(raw, []byte("shadow"), 0); !errors.Is(err, syscall.EACCES) {
			t.Fatalf("reserved raw write: %v", err)
		}
	}
	if f.Stats()["open_files"] != 0 || len(f.PendingFiles()) != 0 {
		t.Fatal("reserved names accepted a journal/state")
	}
	data, err := f.client.ReadFile(context.Background(), f.vol.ID, ".modalfs/owned", 1024)
	if err != nil || string(data) != "metadata" {
		t.Fatalf("internal data changed: %q %v", data, err)
	}
}

func TestRootWritesCannotCreateJournal(t *testing.T) {
	f := newFakeFS(t, Options{})
	if _, err := f.WriteAt("/", []byte("not a file"), 0); !errors.Is(err, syscall.EISDIR) {
		t.Fatalf("root write: %v", err)
	}
	if err := f.Truncate("/", 1); !errors.Is(err, syscall.EISDIR) {
		t.Fatalf("root truncate: %v", err)
	}
	if err := f.Create("/", true); err == nil {
		t.Fatal("root create succeeded")
	}
	if f.Stats()["open_files"] != 0 || len(f.PendingFiles()) != 0 {
		t.Fatal("root accepted a journal/state")
	}
}
