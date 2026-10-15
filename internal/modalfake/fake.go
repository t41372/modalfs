// Package modalfake is a stand-in for Modal's volume API, for tests and for
// trying modalfs without an account. It serves the gRPC calls modalfs makes
// (internal/modalapi) and the signed block URLs, with Volume v2's semantics:
// files are lists of SHA-256 hashes of 8 MiB blocks in a content-addressed
// store, VolumePutFiles2 reports the blocks it doesn't have, and copies are
// manifest-level.
//
// Blocks live on disk, so a test can write gigabytes. Latency and bandwidth
// can be limited, to reproduce what a slow network does to a mount.
package modalfake

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
)

// BlockSize is Volume v2's block size.
const BlockSize = 8 << 20

// Options shape how the fake behaves on the network.
type Options struct {
	// Latency is added to every gRPC call and every block transfer.
	Latency time.Duration
	// UpBytesPerSec and DownBytesPerSec limit block uploads and downloads,
	// shared by all transfers like one network link. 0 means unlimited.
	UpBytesPerSec, DownBytesPerSec int64
	// TrimZeros makes block GETs omit trailing zero bytes, as Modal may when
	// the client pads blocks.
	TrimZeros bool
}

// Server is a running fake. Its URL goes in modalapi.Config.ServerURL.
type Server struct {
	URL string // gRPC endpoint, http://127.0.0.1:port

	opt     Options
	dir     string // block store
	blobURL string // http://127.0.0.1:port for block GET/PUT
	grpcSrv *grpc.Server
	httpSrv *http.Server
	up      *limiter
	down    *limiter

	received atomic.Int64 // block bytes uploaded to the fake
	served   atomic.Int64 // block bytes downloaded from the fake

	mu      sync.Mutex
	volumes map[string]*volume // by ID
	// Like Modal, a block uploaded but not in a committed file yet counts
	// for a commit only with a receipt its upload returned.
	receipts map[string]map[string]bool // hex hash -> receipts handed out

	pb.UnimplementedModalClientServer
}

type volume struct {
	id, name, env string
	created       float64
	files         map[string]*file // path without leading slash
}

type file struct {
	size   uint64
	mtime  uint64
	hashes [][]byte
	mode   uint32
}

// Start runs a fake on loopback ports, storing blocks in dir.
func Start(dir string, opt Options) (*Server, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Server{opt: opt, dir: dir, volumes: map[string]*volume{}, up: newLimiter(opt.UpBytesPerSec), down: newLimiter(opt.DownBytesPerSec)}

	gl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	hl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		gl.Close()
		return nil, err
	}
	s.URL = "http://" + gl.Addr().String()
	s.blobURL = "http://" + hl.Addr().String()

	s.grpcSrv = grpc.NewServer(grpc.MaxRecvMsgSize(100<<20), grpc.MaxSendMsgSize(100<<20), grpc.UnaryInterceptor(s.delay))
	pb.RegisterModalClientServer(s.grpcSrv, s)
	go func() { _ = s.grpcSrv.Serve(gl) }()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /get/{hash}", s.getBlock)
	mux.HandleFunc("PUT /put/{hash}", s.putBlock)
	s.httpSrv = &http.Server{Handler: mux}
	go func() { _ = s.httpSrv.Serve(hl) }()
	return s, nil
}

// Close stops the servers. The block store is left for the caller to remove.
func (s *Server) Close() {
	s.grpcSrv.Stop()
	_ = s.httpSrv.Close()
}

// SetOptions changes latency and bandwidth while running.
func (s *Server) SetOptions(opt Options) {
	s.mu.Lock()
	s.opt = opt
	s.mu.Unlock()
	s.up.setRate(opt.UpBytesPerSec)
	s.down.setRate(opt.DownBytesPerSec)
}

func (s *Server) latency() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opt.Latency
}

func (s *Server) delay(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	if d := s.latency(); d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return h(ctx, req)
}

// ---------- accounts ----------

func (s *Server) TokenInfoGet(context.Context, *pb.TokenInfoGetRequest) (*pb.TokenInfoGetResponse, error) {
	return pb.TokenInfoGetResponse_builder{TokenId: "ak-fake", WorkspaceId: "ac-fake", WorkspaceName: "fake-workspace", UserIdentity: pb.UserIdentity_builder{Username: "fake-user"}.Build()}.Build(), nil
}

func (s *Server) EnvironmentList(context.Context, *emptypb.Empty) (*pb.EnvironmentListResponse, error) {
	return pb.EnvironmentListResponse_builder{Items: []*pb.EnvironmentListItem{pb.EnvironmentListItem_builder{Name: "main", Default: true}.Build()}}.Build(), nil
}

// ---------- volumes ----------

func newID() string {
	b := make([]byte, 11)
	_, _ = rand.Read(b)
	return "vo-" + hex.EncodeToString(b)
}

// defaultEnv is the environment a request without one is for, as with
// Modal: the workspace's default.
const defaultEnv = "main"

func envOrDefault(env string) string {
	if env == "" {
		return defaultEnv
	}
	return env
}

// CreateVolume makes a v2 volume directly, in the default environment, for
// test setup.
func (s *Server) CreateVolume(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := &volume{id: newID(), name: name, env: defaultEnv, created: float64(time.Now().Unix()), files: map[string]*file{}}
	s.volumes[v.id] = v
	return v.id
}

func (s *Server) vol(id string) (*volume, error) {
	v := s.volumes[id]
	if v == nil {
		return nil, status.Errorf(codes.NotFound, "volume %s not found", id)
	}
	return v, nil
}

func (s *Server) byName(name, env string) *volume {
	for _, v := range s.volumes {
		if v.name == name && v.env == envOrDefault(env) {
			return v
		}
	}
	return nil
}

func meta(v *volume) *pb.VolumeMetadata {
	return pb.VolumeMetadata_builder{Version: pb.VolumeFsVersion_VOLUME_FS_VERSION_V2, CreationInfo: pb.CreationInfo_builder{CreatedAt: v.created, CreatedBy: "fake-user"}.Build()}.Build()
}

func (s *Server) VolumeGetOrCreate(_ context.Context, r *pb.VolumeGetOrCreateRequest) (*pb.VolumeGetOrCreateResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.byName(r.GetDeploymentName(), r.GetEnvironmentName())
	create := r.GetObjectCreationType()
	if v != nil && create == pb.ObjectCreationType_OBJECT_CREATION_TYPE_CREATE_FAIL_IF_EXISTS {
		return nil, status.Errorf(codes.AlreadyExists, "Volume '%s' already exists", r.GetDeploymentName())
	}
	if v == nil {
		if create != pb.ObjectCreationType_OBJECT_CREATION_TYPE_CREATE_IF_MISSING && create != pb.ObjectCreationType_OBJECT_CREATION_TYPE_CREATE_FAIL_IF_EXISTS {
			return nil, status.Errorf(codes.NotFound, "Volume '%s' not found", r.GetDeploymentName())
		}
		v = &volume{id: newID(), name: r.GetDeploymentName(), env: envOrDefault(r.GetEnvironmentName()), created: float64(time.Now().Unix()), files: map[string]*file{}}
		s.volumes[v.id] = v
	}
	return pb.VolumeGetOrCreateResponse_builder{VolumeId: v.id, Metadata: meta(v)}.Build(), nil
}

func (s *Server) VolumeList(_ context.Context, r *pb.VolumeListRequest) (*pb.VolumeListResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var items []*pb.VolumeListItem
	for _, v := range s.volumes {
		if v.env == envOrDefault(r.GetEnvironmentName()) {
			items = append(items, pb.VolumeListItem_builder{Label: v.name, VolumeId: v.id, CreatedAt: v.created, Metadata: meta(v)}.Build())
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].GetLabel() < items[j].GetLabel() })
	return pb.VolumeListResponse_builder{Items: items}.Build(), nil
}

func (s *Server) VolumeRename(_ context.Context, r *pb.VolumeRenameRequest) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.vol(r.GetVolumeId())
	if err != nil {
		return nil, err
	}
	v.name = r.GetName()
	return &emptypb.Empty{}, nil
}

func (s *Server) VolumeDelete(_ context.Context, r *pb.VolumeDeleteRequest) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.vol(r.GetVolumeId()); err != nil {
		return nil, err
	}
	delete(s.volumes, r.GetVolumeId())
	return &emptypb.Empty{}, nil
}

// ---------- files ----------

func clean(p string) string { return strings.Trim(path.Clean("/"+p), "/") }

// isDir reports whether p is the root or a prefix of some file path.
func (v *volume) isDir(p string) bool {
	if p == "" {
		return true
	}
	for fp := range v.files {
		if strings.HasPrefix(fp, p+"/") {
			return true
		}
	}
	return false
}

func entry(p string, f *file) *pb.FileEntry {
	if f == nil {
		return pb.FileEntry_builder{Path: p, Type: pb.FileEntry_DIRECTORY}.Build()
	}
	return pb.FileEntry_builder{Path: p, Type: pb.FileEntry_FILE, Size: f.size, Mtime: f.mtime}.Build()
}

func (s *Server) VolumeListFiles2(r *pb.VolumeListFiles2Request, stream grpc.ServerStreamingServer[pb.VolumeListFiles2Response]) error {
	if d := s.latency(); d > 0 {
		time.Sleep(d)
	}
	s.mu.Lock()
	v, err := s.vol(r.GetVolumeId())
	if err != nil {
		s.mu.Unlock()
		return err
	}
	p := clean(r.GetPath())
	var out []*pb.FileEntry
	if f := v.files[p]; f != nil {
		out = append(out, entry(p, f)) // listing a file returns the file itself
	} else if !v.isDir(p) {
		s.mu.Unlock()
		return status.Errorf(codes.NotFound, "No such file or directory: %s", r.GetPath())
	} else {
		seen := map[string]bool{}
		prefix := ""
		if p != "" {
			prefix = p + "/"
		}
		for fp, f := range v.files {
			rest, ok := strings.CutPrefix(fp, prefix)
			if !ok {
				continue
			}
			if r.GetRecursive() {
				out = append(out, entry(fp, f))
				continue
			}
			if i := strings.IndexByte(rest, '/'); i >= 0 {
				d := prefix + rest[:i]
				if !seen[d] {
					seen[d] = true
					out = append(out, entry(d, nil))
				}
			} else {
				out = append(out, entry(fp, f))
			}
		}
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].GetPath() < out[j].GetPath() })
	for len(out) > 0 {
		n := min(len(out), 100)
		if err := stream.Send(pb.VolumeListFiles2Response_builder{Entries: out[:n]}.Build()); err != nil {
			return err
		}
		out = out[n:]
	}
	return nil
}

func (s *Server) VolumeGetFile2(_ context.Context, r *pb.VolumeGetFile2Request) (*pb.VolumeGetFile2Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.vol(r.GetVolumeId())
	if err != nil {
		return nil, err
	}
	p := clean(r.GetPath())
	f := v.files[p]
	if f == nil {
		return nil, status.Errorf(codes.NotFound, "No such file: %s", r.GetPath())
	}
	start, n := r.GetStart(), r.GetLen()
	end := f.size
	if n > 0 && start+n < end {
		end = start + n
	}
	var urls []string
	for i := start / BlockSize; i*BlockSize < end && int(i) < len(f.hashes); i++ {
		blen := min(f.size-i*BlockSize, BlockSize)
		urls = append(urls, fmt.Sprintf("%s/get/%x?len=%d", s.blobURL, f.hashes[i], blen))
	}
	return pb.VolumeGetFile2Response_builder{GetUrls: urls, Size: f.size, Start: start, Len: end - min(start, end)}.Build(), nil
}

func (s *Server) blockPath(hash []byte) string {
	h := hex.EncodeToString(hash)
	return filepath.Join(s.dir, h[:2], h)
}

func (s *Server) hasBlock(hash []byte) bool {
	_, err := os.Stat(s.blockPath(hash))
	return err == nil
}

func (s *Server) VolumePutFiles2(_ context.Context, r *pb.VolumePutFiles2Request) (*pb.VolumePutFiles2Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.vol(r.GetVolumeId())
	if err != nil {
		return nil, err
	}
	var missing []*pb.VolumePutFiles2Response_MissingBlock
	var vouched []string
	for fi, f := range r.GetFiles() {
		for _, component := range strings.Split(clean(f.GetPath()), "/") {
			if len(component) > 255 {
				return nil, status.Error(codes.InvalidArgument, "path component exceeds 255 bytes")
			}
		}
		nblocks := (f.GetSize() + BlockSize - 1) / BlockSize
		if uint64(len(f.GetBlocks())) != nblocks {
			return nil, status.Errorf(codes.InvalidArgument, "%s: %d blocks for size %d", f.GetPath(), len(f.GetBlocks()), f.GetSize())
		}
		for bi, b := range f.GetBlocks() {
			h := hex.EncodeToString(b.GetContentsSha256())
			if rs, unconfirmed := s.receipts[h]; unconfirmed && rs[string(b.GetPutResponse())] {
				vouched = append(vouched, h)
				continue
			}
			if _, unconfirmed := s.receipts[h]; unconfirmed || !s.hasBlock(b.GetContentsSha256()) {
				missing = append(missing, pb.VolumePutFiles2Response_MissingBlock_builder{
					FileIndex: uint64(fi), BlockIndex: uint64(bi),
					PutUrl: fmt.Sprintf("%s/put/%x", s.blobURL, b.GetContentsSha256()),
				}.Build())
			}
		}
	}
	if len(missing) > 0 {
		return pb.VolumePutFiles2Response_builder{MissingBlocks: missing}.Build(), nil
	}
	for _, h := range vouched {
		delete(s.receipts, h) // in a committed file now
	}
	now := uint64(time.Now().Unix())
	for _, f := range r.GetFiles() {
		p := clean(f.GetPath())
		if p == "" || v.isDir(p) {
			return nil, status.Errorf(codes.InvalidArgument, "%s is a directory", f.GetPath())
		}
		var hashes [][]byte
		for _, b := range f.GetBlocks() {
			hashes = append(hashes, b.GetContentsSha256())
		}
		v.files[p] = &file{size: f.GetSize(), mtime: now, hashes: hashes, mode: f.GetMode()}
	}
	return pb.VolumePutFiles2Response_builder{}.Build(), nil
}

func (s *Server) VolumeCopyFiles2(_ context.Context, r *pb.VolumeCopyFiles2Request) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.vol(r.GetVolumeId())
	if err != nil {
		return nil, err
	}
	dst := clean(r.GetDstPath())
	// Like `cp`: into dst when it's a directory or there are several sources, else to dst.
	into := len(r.GetSrcPaths()) > 1 || v.isDir(dst) || strings.HasSuffix(r.GetDstPath(), "/")
	now := uint64(time.Now().Unix())
	for _, sp := range r.GetSrcPaths() {
		src := clean(sp)
		target := dst
		if into {
			target = path.Join(dst, path.Base(src))
		}
		if f := v.files[src]; f != nil {
			c := *f
			c.mtime = now
			v.files[target] = &c
			continue
		}
		if !v.isDir(src) {
			return nil, status.Errorf(codes.NotFound, "No such file or directory: %s", sp)
		}
		if !r.GetRecursive() {
			return nil, status.Errorf(codes.InvalidArgument, "%s is a directory; copy it recursively", sp)
		}
		for fp, f := range v.files {
			if rest, ok := strings.CutPrefix(fp, src+"/"); ok {
				c := *f
				c.mtime = now
				v.files[path.Join(target, rest)] = &c
			}
		}
	}
	return &emptypb.Empty{}, nil
}

func (s *Server) VolumeRemoveFile2(_ context.Context, r *pb.VolumeRemoveFile2Request) (*emptypb.Empty, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.vol(r.GetVolumeId())
	if err != nil {
		return nil, err
	}
	p := clean(r.GetPath())
	if v.files[p] != nil {
		delete(v.files, p)
		return &emptypb.Empty{}, nil
	}
	if !v.isDir(p) || p == "" {
		return nil, status.Errorf(codes.NotFound, "No such file or directory: %s", r.GetPath())
	}
	if !r.GetRecursive() {
		return nil, status.Errorf(codes.FailedPrecondition, "%s is a directory; remove it recursively", r.GetPath())
	}
	for fp := range v.files {
		if strings.HasPrefix(fp, p+"/") {
			delete(v.files, fp)
		}
	}
	return &emptypb.Empty{}, nil
}

// ---------- block store ----------

func (s *Server) getBlock(w http.ResponseWriter, r *http.Request) {
	hash, err := hex.DecodeString(r.PathValue("hash"))
	if err != nil {
		http.Error(w, "bad hash", 400)
		return
	}
	data, err := os.ReadFile(s.blockPath(hash))
	if err != nil {
		http.Error(w, "no such block", 404)
		return
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("len")); err == nil && n < len(data) {
		data = data[:n]
	}
	s.mu.Lock()
	trim := s.opt.TrimZeros
	s.mu.Unlock()
	if trim {
		for len(data) > 0 && data[len(data)-1] == 0 {
			data = data[:len(data)-1]
		}
	}
	if d := s.latency(); d > 0 {
		time.Sleep(d)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	n, _ := io.Copy(w, s.down.reader(r.Context(), bytes.NewReader(data)))
	s.served.Add(n)
}

func (s *Server) putBlock(w http.ResponseWriter, r *http.Request) {
	want, err := hex.DecodeString(r.PathValue("hash"))
	if err != nil {
		http.Error(w, "bad hash", 400)
		return
	}
	if d := s.latency(); d > 0 {
		time.Sleep(d)
	}
	data, err := io.ReadAll(&counting{s.up.reader(r.Context(), io.LimitReader(r.Body, BlockSize+1)), &s.received})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if len(data) > BlockSize {
		http.Error(w, "block too large", 400)
		return
	}
	got := sha256.Sum256(data)
	if hex.EncodeToString(got[:]) != hex.EncodeToString(want) {
		http.Error(w, "hash mismatch", 400)
		return
	}
	p := s.blockPath(want)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err == nil {
		tmp, err := os.CreateTemp(filepath.Dir(p), "put-*")
		if err == nil {
			_, err = tmp.Write(data)
			tmp.Close()
			if err == nil {
				err = os.Rename(tmp.Name(), p)
			}
			if err != nil {
				_ = os.Remove(tmp.Name())
			}
		}
	}
	receipt := "receipt-" + rand.Text()
	s.mu.Lock()
	if s.receipts == nil {
		s.receipts = map[string]map[string]bool{}
	}
	h := hex.EncodeToString(want)
	if s.receipts[h] == nil && !s.committedLocked(h) {
		s.receipts[h] = map[string]bool{}
	}
	if s.receipts[h] != nil {
		s.receipts[h][receipt] = true
	}
	s.mu.Unlock()
	_, _ = w.Write([]byte(receipt))
}

// committedLocked: a committed file holds the block.
func (s *Server) committedLocked(h string) bool {
	for _, v := range s.volumes {
		for _, f := range v.files {
			for _, fh := range f.hashes {
				if hex.EncodeToString(fh) == h {
					return true
				}
			}
		}
	}
	return false
}

// Received is how many block bytes have been uploaded so far.
func (s *Server) Received() int64 { return s.received.Load() }

// Served is how many block bytes have been downloaded so far.
func (s *Server) Served() int64 { return s.served.Load() }

// ---------- bandwidth ----------

// limiter is a shared token bucket: all readers through it together get at
// most rate bytes per second, like transfers sharing one link.
type limiter struct {
	mu   sync.Mutex
	rate int64
	next time.Time // when the link is free again
}

func newLimiter(rate int64) *limiter { return &limiter{rate: rate} }

func (l *limiter) setRate(rate int64) {
	l.mu.Lock()
	l.rate = rate
	l.mu.Unlock()
}

// wait reserves n bytes of link time and sleeps until they've "passed".
func (l *limiter) wait(ctx context.Context, n int) error {
	l.mu.Lock()
	if l.rate <= 0 {
		l.mu.Unlock()
		return nil
	}
	now := time.Now()
	if l.next.Before(now) {
		l.next = now
	}
	l.next = l.next.Add(time.Duration(float64(n) / float64(l.rate) * float64(time.Second)))
	until := l.next
	l.mu.Unlock()
	select {
	case <-time.After(time.Until(until)):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type limitedReader struct {
	ctx context.Context
	l   *limiter
	r   io.Reader
}

func (l *limiter) reader(ctx context.Context, r io.Reader) io.Reader {
	return &limitedReader{ctx: ctx, l: l, r: r}
}

func (lr *limitedReader) Read(p []byte) (int, error) {
	if len(p) > 64<<10 {
		p = p[:64<<10] // small steps, so concurrent transfers interleave
	}
	n, err := lr.r.Read(p)
	if n > 0 {
		if werr := lr.l.wait(lr.ctx, n); werr != nil {
			return n, werr
		}
	}
	return n, err
}

// counting counts the bytes read through it as they arrive, so a test can
// tell an upload has begun before its block is complete.
type counting struct {
	r io.Reader
	n *atomic.Int64
}

func (c *counting) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n.Add(int64(n))
	return n, err
}
