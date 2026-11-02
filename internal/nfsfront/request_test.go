package nfsfront

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	nfs "github.com/willscott/go-nfs"
	"github.com/willscott/go-nfs-client/nfs/rpc"
	"github.com/willscott/go-nfs-client/nfs/xdr"
	nfsfile "github.com/willscott/go-nfs/file"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

type namespaceRPC struct {
	t       *testing.T
	handler *handler
	root    []byte
	addr    string
}

func newNamespaceRPC(t *testing.T, b *billyFS, override func(*handler) nfs.Handler) *namespaceRPC {
	t.Helper()
	h := newHandler(b)
	var served nfs.Handler = h
	if override != nil {
		served = override(h)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go nfs.Serve(ln, served)
	return &namespaceRPC{t, h, h.ToHandle(b, nil), ln.Addr().String()}
}
func (r *namespaceRPC) header(proc uint32) rpc.Header {
	return rpc.Header{Rpcvers: 2, Prog: 100003, Vers: 3, Proc: proc, Cred: unixCred("names", uint32(os.Getuid()), uint32(os.Getgid()))}
}
func (r *namespaceRPC) request(request any) ([]byte, error) {
	client, err := rpc.DialTCP("tcp", r.addr, false)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	response, err := client.Call(request)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(response)
}
func (r *namespaceRPC) call(request any) []byte {
	r.t.Helper()
	data, err := r.request(request)
	if err != nil {
		r.t.Fatal(err)
	}
	if len(data) < 4 {
		r.t.Fatalf("short RPC: %x", data)
	}
	return data
}

type rpcResult struct {
	data []byte
	err  error
}
type namedDirArg struct {
	Handle []byte
	Name   string
}

func checkRPCResult(t *testing.T, result rpcResult) {
	t.Helper()
	if result.err != nil || len(result.data) < 4 || binary.BigEndian.Uint32(result.data) != 0 {
		t.Fatalf("RPC failed: %x %v", result.data, result.err)
	}
}

type pausedRawServer struct {
	*handler
	entered, release, guarded chan struct{}
	once                      sync.Once
	guardOnce                 sync.Once
}

func (p *pausedRawServer) BeginRequest(header rpc.Header) (nfs.Handler, func()) {
	request, finish := p.handler.BeginRequest(header)
	scoped, ok := request.(*requestHandler)
	if !ok {
		return request, finish
	}
	if header.Proc == 7 {
		return &pausedWriteRequest{requestHandler: scoped, server: p}, finish
	}
	if header.Proc == 14 {
		return &observedRenameRequest{requestHandler: scoped, server: p}, finish
	}
	return request, finish
}

type pausedWriteRequest struct {
	*requestHandler
	server *pausedRawServer
}

func (p *pausedWriteRequest) FromHandle(handle []byte) (billy.Filesystem, []string, error) {
	fs, parts, err := p.requestHandler.FromHandle(handle)
	if err == nil {
		p.server.once.Do(func() { close(p.server.entered); <-p.server.release })
	}
	return fs, parts, err
}

type observedRenameRequest struct {
	*requestHandler
	server *pausedRawServer
}

func (p *observedRenameRequest) GuardRename(fs billy.Filesystem, oldPath, newPath []string) func(error) {
	p.server.guardOnce.Do(func() { close(p.server.guarded) })
	return p.handler.GuardRename(fs, oldPath, newPath)
}

func TestRawWritePinsIdentityAcrossConcurrentReplacement(t *testing.T) {
	b, fake, c, id := newNamespaceFS(t, time.Hour)
	seedNamespaceFile(t, fake, id, "source", "source")
	seedNamespaceFile(t, fake, id, "target", "target")
	seedNamespaceFile(t, fake, id, "unrelated", "other")
	probe := &pausedRawServer{entered: make(chan struct{}), release: make(chan struct{}), guarded: make(chan struct{})}
	r := newNamespaceRPC(t, b, func(h *handler) nfs.Handler { probe.handler = h; return probe })
	target := r.handler.ToHandle(b, []string{"target"})
	unrelated := r.handler.ToHandle(b, []string{"unrelated"})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(probe.release) }) }
	defer release()
	writeDone := make(chan rpcResult, 1)
	go func() {
		data, err := r.request(struct {
			rpc.Header
			Handle     []byte
			Offset     uint64
			Count, How uint32
			Data       []byte
		}{r.header(7), target, 0, 4, 2, []byte("edit")})
		writeDone <- rpcResult{data, err}
	}()
	select {
	case <-probe.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("WRITE did not acquire its raw binding")
	}
	renameDone := make(chan rpcResult, 1)
	go func() {
		data, err := r.request(struct {
			rpc.Header
			From, To namedDirArg
		}{r.header(14), namedDirArg{r.root, "source"}, namedDirArg{r.root, "target"}})
		renameDone <- rpcResult{data, err}
	}()
	select {
	case <-probe.guarded:
	case <-time.After(2 * time.Second):
		t.Fatal("rename did not reach affected-binding guard")
	}
	select {
	case result := <-renameDone:
		t.Fatalf("replacement passed a live raw request: %x %v", result.data, result.err)
	default:
	}
	commitDone := make(chan rpcResult, 1)
	go func() {
		data, err := r.request(struct {
			rpc.Header
			Handle []byte
			Offset uint64
			Count  uint32
		}{r.header(21), unrelated, 0, 0})
		commitDone <- rpcResult{data, err}
	}()
	select {
	case result := <-commitDone:
		checkRPCResult(t, result)
	case <-time.After(time.Second):
		t.Fatal("unrelated raw COMMIT stalled behind rename")
	}
	release()
	select {
	case result := <-writeDone:
		checkRPCResult(t, result)
	case <-time.After(3 * time.Second):
		t.Fatal("WRITE did not finish")
	}
	select {
	case result := <-renameDone:
		checkRPCResult(t, result)
	case <-time.After(3 * time.Second):
		t.Fatal("rename did not finish")
	}
	data, err := c.ReadFile(context.Background(), id, "target", 1024)
	if err != nil || string(data) != "source" {
		t.Fatalf("late old-target write corrupted successor: %q %v", data, err)
	}
	r.handler.ToHandle(b, []string{"target"})
	if _, _, err := r.handler.FromHandle(target); err == nil {
		t.Fatal("old overwritten target handle revived")
	}
}

type pausedLookupServer struct {
	*handler
	entered, release chan struct{}
	once             sync.Once
}

func (p *pausedLookupServer) BeginRequest(header rpc.Header) (nfs.Handler, func()) {
	request, finish := p.handler.BeginRequest(header)
	if scoped, ok := request.(*requestHandler); ok && header.Proc == 3 {
		return &pausedLookupRequest{requestHandler: scoped, server: p}, finish
	}
	return request, finish
}

type pausedLookupRequest struct {
	*requestHandler
	server *pausedLookupServer
}

func (p *pausedLookupRequest) ToHandle(fs billy.Filesystem, parts []string) []byte {
	if strings.Join(parts, "/") == "source" {
		p.server.once.Do(func() { close(p.server.entered); <-p.server.release })
	}
	return p.handler.ToHandle(fs, parts)
}

func TestLookupPublishesIdentityBeforeConcurrentRename(t *testing.T) {
	b, fake, _, id := newNamespaceFS(t, time.Hour)
	seedNamespaceFile(t, fake, id, "source", "object")
	probe := &pausedLookupServer{entered: make(chan struct{}), release: make(chan struct{})}
	r := newNamespaceRPC(t, b, func(h *handler) nfs.Handler { probe.handler = h; return probe })
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(probe.release) }) }
	defer release()
	lookupDone := make(chan rpcResult, 1)
	go func() {
		data, err := r.request(struct {
			rpc.Header
			Where namedDirArg
		}{r.header(3), namedDirArg{r.root, "source"}})
		lookupDone <- rpcResult{data, err}
	}()
	select {
	case <-probe.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("lookup did not reach publication point")
	}
	renameDone := make(chan rpcResult, 1)
	go func() {
		data, err := r.request(struct {
			rpc.Header
			From, To namedDirArg
		}{r.header(14), namedDirArg{r.root, "source"}, namedDirArg{r.root, "moved"}})
		renameDone <- rpcResult{data, err}
	}()
	select {
	case result := <-renameDone:
		t.Fatalf("rename raced past unpublished lookup: %x %v", result.data, result.err)
	case <-time.After(30 * time.Millisecond):
	}
	release()
	result := <-lookupDone
	checkRPCResult(t, result)
	length := int(binary.BigEndian.Uint32(result.data[4:8]))
	handle := result.data[8 : 8+length]
	result = <-renameDone
	checkRPCResult(t, result)
	fs, parts, err := r.handler.FromHandle(handle)
	if err != nil || strings.Join(parts, "/") != "moved" {
		t.Fatalf("late lookup bound obsolete name: %q %v", parts, err)
	}
	f, err := fs.Open(fs.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(f)
	if err != nil || string(data) != "object" {
		t.Fatalf("late lookup object: %q %v", data, err)
	}
}

func TestCachedFileInfoNeverSubstitutesSuccessorIdentity(t *testing.T) {
	b, fake, _, id := newNamespaceFS(t, time.Hour)
	seedNamespaceFile(t, fake, id, "file", "old")
	h := newHandler(b)
	info, err := b.Stat("file")
	if err != nil {
		t.Fatal(err)
	}
	old := h.ToHandleInfo(b, []string{"file"}, info)
	h.Removed(b, []string{"file"})
	next := h.ToHandle(b, []string{"file"})
	cached := h.ToHandleInfo(b, []string{"file"}, info)
	if !bytes.Equal(cached, old) || bytes.Equal(cached, next) {
		t.Fatal("cached listing identity changed to a successor")
	}
	if _, _, err := h.FromHandle(cached); err == nil {
		t.Fatal("cached listing revived removed identity")
	}
}

func TestRenamedDirectoryHandleKeepsObjectAfterOldPathRecreation(t *testing.T) {
	b, fake, c, id := newNamespaceFS(t, time.Hour)
	seedNamespaceFile(t, fake, id, "source/nested/file", "source")
	r := newNamespaceRPC(t, b, nil)
	handle := r.handler.ToHandle(b, []string{"source", "nested", "file"})
	inode := binary.BigEndian.Uint64(handle[:8])
	rename := r.call(struct {
		rpc.Header
		From, To namedDirArg
	}{r.header(14), namedDirArg{r.root, "source"}, namedDirArg{r.root, "moved"}})
	checkRPCResult(t, rpcResult{rename, nil})
	seedNamespaceFile(t, fake, id, "source/nested/file", "other!")
	fs, parts, err := r.handler.FromHandle(handle)
	if err != nil || strings.Join(parts, "/") != "moved/nested/file" {
		t.Fatalf("descendant wasn't relocated: %q %v", parts, err)
	}
	info, err := fs.Stat(fs.Join(parts...))
	if err != nil {
		t.Fatal(err)
	}
	if actual := info.Sys().(*nfsfile.FileInfo).Fileid; actual != inode {
		t.Fatalf("file ID changed across rename: %d -> %d", inode, actual)
	}
	read := r.call(struct {
		rpc.Header
		Handle []byte
		Offset uint64
		Count  uint32
	}{r.header(6), handle, 0, 6})
	var reply struct {
		Status uint32
		Attrs  *nfs.FileAttribute `xdr:"optional"`
		Count  uint32
		EOF    bool
		Data   []byte
	}
	if err := xdr.Read(bytes.NewReader(read), &reply); err != nil || reply.Status != 0 || string(reply.Data) != "source" {
		t.Fatalf("old source handle read successor: %+v %v", reply, err)
	}
	write := r.call(struct {
		rpc.Header
		Handle     []byte
		Offset     uint64
		Count, How uint32
		Data       []byte
	}{r.header(7), handle, 0, 6, 2, []byte("edited")})
	checkRPCResult(t, rpcResult{write, nil})
	if err := b.fs.Flush("moved/nested/file"); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"moved/nested/file": "edited", "source/nested/file": "other!"} {
		actual, err := c.ReadFile(context.Background(), id, name, 1024)
		if err != nil || string(actual) != want {
			t.Fatalf("wrong file affected at %s: %q %v", name, actual, err)
		}
	}
}

type rejectRenameSourceRemoval struct{ pb.ModalClientClient }

func (g rejectRenameSourceRemoval) VolumeRemoveFile2(ctx context.Context, req *pb.VolumeRemoveFile2Request, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	if req.GetPath() == "source" {
		return nil, status.Error(codes.PermissionDenied, "injected removal failure")
	}
	return g.ModalClientClient.VolumeRemoveFile2(ctx, req, opts...)
}
func TestPartialRenameRetiresOnlyReplacedDestination(t *testing.T) {
	b, fake, c, id := newNamespaceFS(t, time.Hour)
	seedNamespaceFile(t, fake, id, "source", "source")
	seedNamespaceFile(t, fake, id, "target", "target")
	r := newNamespaceRPC(t, b, nil)
	source := r.handler.ToHandle(b, []string{"source"})
	target := r.handler.ToHandle(b, []string{"target"})
	original := c.Stub
	c.Stub = rejectRenameSourceRemoval{original}
	defer func() { c.Stub = original }()
	result := r.call(struct {
		rpc.Header
		From, To namedDirArg
	}{r.header(14), namedDirArg{r.root, "source"}, namedDirArg{r.root, "target"}})
	if binary.BigEndian.Uint32(result) == 0 {
		t.Fatal("partial rename reported success")
	}
	if _, _, err := r.handler.FromHandle(target); err == nil {
		t.Fatal("old replaced target identity survived partial publication")
	}
	_, parts, err := r.handler.FromHandle(source)
	if err != nil || strings.Join(parts, "/") != "source" {
		t.Fatalf("failed rename moved source identity: %q %v", parts, err)
	}
	lookup := r.call(struct {
		rpc.Header
		Where namedDirArg
	}{r.header(3), namedDirArg{r.root, "target"}})
	checkRPCResult(t, rpcResult{lookup, nil})
	if _, _, err := r.handler.FromHandle(target); err == nil {
		t.Fatal("new lookup revived old destination")
	}
	for _, name := range []string{"source", "target"} {
		actual, err := c.ReadFile(context.Background(), id, name, 1024)
		if err != nil || string(actual) != "source" {
			t.Fatalf("partial copy content at %s: %q %v", name, actual, err)
		}
	}
}
