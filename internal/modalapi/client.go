// Package modalapi is a thin client for the Modal Volume v2 block API.
//
// It talks directly to Modal's control plane (the same gRPC service the
// official SDKs use) and to the block store (signed HTTPS URLs). It never
// starts any Modal compute.
package modalapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/modal-labs/modal-client/go/proto/modal_proto"
	toml "github.com/pelletier/go-toml/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// BlockSize is the fixed Volume v2 block size.
const BlockSize = 8 << 20

const clientVersion = "1.6.1"

type Config struct {
	Profile     string
	Environment string
	ServerURL   string
	TokenID     string
	TokenSecret string
	// Transport: "grpc" (default, HTTP/2) or "http1".
	Transport string
}

// Profile is one entry of ~/.modal.toml.
type Profile struct {
	Name        string
	TokenID     string
	TokenSecret string
	ServerURL   string
	Environment string
	Active      bool
}

// ModalConfigPath is where the Modal CLI keeps its profiles.
func ModalConfigPath() string {
	if p := os.Getenv("MODAL_CONFIG_PATH"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".modal.toml")
}

// ReadProfiles parses ~/.modal.toml (read-only; modalfs never writes it).
func ReadProfiles() ([]Profile, error) {
	raw, err := os.ReadFile(ModalConfigPath())
	if err != nil {
		return nil, err
	}
	var all map[string]map[string]any
	if err := toml.Unmarshal(raw, &all); err != nil {
		return nil, err
	}
	var out []Profile
	for name, p := range all {
		pr := Profile{Name: name}
		pr.TokenID, _ = p["token_id"].(string)
		pr.TokenSecret, _ = p["token_secret"].(string)
		pr.ServerURL, _ = p["server_url"].(string)
		pr.Environment, _ = p["environment"].(string)
		pr.Active, _ = p["active"].(bool)
		if pr.TokenID != "" {
			out = append(out, pr)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// LoadConfig resolves credentials the same way the Modal CLI does:
// env MODAL_TOKEN_ID / MODAL_TOKEN_SECRET / MODAL_SERVER_URL / MODAL_ENVIRONMENT
// override the active (or named) profile in ~/.modal.toml (or $MODAL_CONFIG_PATH).
func LoadConfig(profile string) (Config, error) {
	cfg := Config{ServerURL: "https://api.modal.com"}
	path := os.Getenv("MODAL_CONFIG_PATH")
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".modal.toml")
	}
	if profile == "" {
		profile = os.Getenv("MODAL_PROFILE")
	}
	if raw, err := os.ReadFile(path); err == nil {
		var all map[string]map[string]any
		if err := toml.Unmarshal(raw, &all); err != nil {
			return cfg, fmt.Errorf("parse %s: %w", path, err)
		}
		if profile == "" {
			for name, p := range all {
				if a, _ := p["active"].(bool); a {
					profile = name
				}
			}
			if profile == "" && len(all) == 1 {
				for name := range all {
					profile = name
				}
			}
		}
		if p, ok := all[profile]; ok {
			cfg.Profile = profile
			cfg.TokenID, _ = p["token_id"].(string)
			cfg.TokenSecret, _ = p["token_secret"].(string)
			if s, _ := p["server_url"].(string); s != "" {
				cfg.ServerURL = s
			}
			cfg.Environment, _ = p["environment"].(string)
		}
	}
	if v := os.Getenv("MODAL_TOKEN_ID"); v != "" {
		cfg.TokenID = v
	}
	if v := os.Getenv("MODAL_TOKEN_SECRET"); v != "" {
		cfg.TokenSecret = v
	}
	if v := os.Getenv("MODAL_SERVER_URL"); v != "" {
		cfg.ServerURL = v
	}
	if v := os.Getenv("MODAL_ENVIRONMENT"); v != "" {
		cfg.Environment = v
	}
	cfg.Transport = os.Getenv("MODALFS_TRANSPORT")
	if cfg.TokenID == "" && cfg.TokenSecret == "" {
		return cfg, errors.New("no Modal credentials: run `modal token new` or set MODAL_TOKEN_ID/MODAL_TOKEN_SECRET")
	}
	return cfg, nil
}

type Client struct {
	cfg  Config
	Stub pb.ModalClientClient
	HTTP *http.Client
	conn *grpc.ClientConn // nil for the HTTP/1.1 transport
	// Byte counters for block data transferred (for status displays).
	BytesDown, BytesUp atomic.Int64
	up                 *upSlots // the block uploads in flight, for every volume
	receipts           receipts // for blocks uploaded but not committed yet (see Ahead)
}

func (c *Client) headers() map[string]string {
	h := map[string]string{
		"x-modal-client-type":    "1", // CLIENT_TYPE_CLIENT
		"x-modal-client-version": clientVersion,
	}
	if c.cfg.TokenID != "" {
		h["x-modal-token-id"] = c.cfg.TokenID
	}
	if c.cfg.TokenSecret != "" {
		h["x-modal-token-secret"] = c.cfg.TokenSecret
	}
	return h
}

func New(cfg Config) (*Client, error) {
	c := &Client{cfg: cfg, up: newUpSlots()}
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     90 * time.Second,
		// HTTP/1.1 only: over HTTP/2 every block shares one connection to
		// the block store, which held uploads and downloads to about
		// 10 MiB/s on a link where separate connections reach 45-50.
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	c.HTTP = &http.Client{Transport: tr}
	if cfg.Transport == "http1" {
		c.Stub = pb.NewModalClientClient(&h1Conn{base: strings.TrimRight(cfg.ServerURL, "/"), hc: &http.Client{Transport: tr, Timeout: 5 * time.Minute}, headers: c.headers})
		return c, nil
	}
	target, isTLS := strings.CutPrefix(cfg.ServerURL, "https://")
	var creds credentials.TransportCredentials
	if isTLS {
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
		if !strings.Contains(target, ":") {
			target += ":443"
		}
	} else if host, ok := strings.CutPrefix(cfg.ServerURL, "http://"); ok && isLoopback(host) {
		// Plain gRPC only to this machine: the fake Modal in internal/modalfake.
		target, creds = host, insecure.NewCredentials()
	} else {
		return nil, fmt.Errorf("unsupported server url %q", cfg.ServerURL)
	}
	add := func(ctx context.Context) context.Context {
		kv := []string{}
		for k, v := range c.headers() {
			kv = append(kv, k, v)
		}
		return metadata.AppendToOutgoingContext(ctx, kv...)
	}
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(creds),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(100<<20), grpc.MaxCallSendMsgSize(100<<20)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 10 * time.Second, PermitWithoutStream: true}),
		grpc.WithUnaryInterceptor(func(ctx context.Context, m string, req, reply any, cc *grpc.ClientConn, inv grpc.UnaryInvoker, o ...grpc.CallOption) error {
			return inv(add(ctx), m, req, reply, cc, o...)
		}),
		grpc.WithStreamInterceptor(func(ctx context.Context, d *grpc.StreamDesc, cc *grpc.ClientConn, m string, s grpc.Streamer, o ...grpc.CallOption) (grpc.ClientStream, error) {
			return s(add(ctx), d, cc, m, o...)
		}),
	)
	if err != nil {
		return nil, err
	}
	c.Stub = pb.NewModalClientClient(conn)
	c.conn = conn
	return c, nil
}

// Close releases the client's connections. Calls after Close fail.
func (c *Client) Close() error {
	if t, ok := c.HTTP.Transport.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

func isLoopback(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

func retryable(err error) bool {
	st, ok := status.FromError(err)
	if !ok {
		return true
	}
	switch st.Code() {
	case codes.Unavailable, codes.Internal, codes.DeadlineExceeded, codes.ResourceExhausted, codes.Unknown:
		return true
	}
	return false
}

func withRetry[T any](ctx context.Context, f func(context.Context) (T, error)) (T, error) {
	var zero T
	delay := 200 * time.Millisecond
	for attempt := 0; ; attempt++ {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		v, err := f(cctx)
		cancel()
		if err == nil {
			return v, nil
		}
		if attempt >= 5 || !retryable(err) || ctx.Err() != nil {
			return zero, err
		}
		time.Sleep(delay)
		delay *= 2
	}
}

// IsNotFound reports whether err is a gRPC NOT_FOUND.
func IsNotFound(err error) bool {
	return status.Code(err) == codes.NotFound
}

// IsUnreachable reports whether err only says Modal couldn't be reached in
// time: no network (as while a Mac wakes from sleep), a timeout, or trouble
// on Modal's side. Trying again later may work.
func IsUnreachable(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded:
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}

type VolumeInfo struct {
	ID        string             `json:"id"`
	Name      string             `json:"name"`
	Version   pb.VolumeFsVersion `json:"-"`
	CreatedAt int64              `json:"created_at,omitempty"`
	CreatedBy string             `json:"created_by,omitempty"`
}

// V2 reports whether the volume uses the v2 block format modalfs supports.
func (v VolumeInfo) V2() bool { return v.Version == pb.VolumeFsVersion_VOLUME_FS_VERSION_V2 }

func (c *Client) ListVolumes(ctx context.Context) ([]VolumeInfo, error) {
	var out []VolumeInfo
	var before float64
	for page := 0; page < 50; page++ {
		req := pb.VolumeListRequest_builder{EnvironmentName: c.cfg.Environment}
		if before > 0 {
			req.Pagination = pb.ListPagination_builder{MaxObjects: 100, CreatedBefore: before}.Build()
		} else {
			req.Pagination = pb.ListPagination_builder{MaxObjects: 100}.Build()
		}
		r, err := withRetry(ctx, func(ctx context.Context) (*pb.VolumeListResponse, error) {
			return c.Stub.VolumeList(ctx, req.Build())
		})
		if err != nil {
			return nil, err
		}
		items := r.GetItems()
		for _, it := range items {
			ci := it.GetMetadata().GetCreationInfo()
			created := ci.GetCreatedAt()
			if created == 0 {
				created = it.GetCreatedAt()
			}
			out = append(out, VolumeInfo{ID: it.GetVolumeId(), Name: it.GetLabel(), Version: it.GetMetadata().GetVersion(),
				CreatedAt: int64(created), CreatedBy: ci.GetCreatedBy()})
			if before == 0 || created < before {
				before = created
			}
		}
		if len(items) < 100 || before == 0 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// LookupVolume resolves a volume by name. create=true creates a v2 volume when missing.
func (c *Client) LookupVolume(ctx context.Context, name string, create bool) (VolumeInfo, error) {
	ct := pb.ObjectCreationType_OBJECT_CREATION_TYPE_UNSPECIFIED
	if create {
		ct = pb.ObjectCreationType_OBJECT_CREATION_TYPE_CREATE_IF_MISSING
	}
	r, err := withRetry(ctx, func(ctx context.Context) (*pb.VolumeGetOrCreateResponse, error) {
		return c.Stub.VolumeGetOrCreate(ctx, pb.VolumeGetOrCreateRequest_builder{
			DeploymentName: name, EnvironmentName: c.cfg.Environment, ObjectCreationType: ct,
			Version: pb.VolumeFsVersion_VOLUME_FS_VERSION_V2,
		}.Build())
	})
	if err != nil {
		return VolumeInfo{}, err
	}
	return VolumeInfo{ID: r.GetVolumeId(), Name: name, Version: r.GetMetadata().GetVersion(), CreatedAt: int64(r.GetMetadata().GetCreationInfo().GetCreatedAt())}, nil
}

type Entry struct {
	Path  string // volume-relative, no leading slash
	IsDir bool
	Type  pb.FileEntry_FileType
	Size  int64
	Mtime int64 // unix seconds
}

// Mode reports the entry's type with synthetic permissions. The public API
// does not expose permissions, even for regular files.
func (e Entry) Mode() os.FileMode {
	if e.IsDir {
		return os.ModeDir | 0o755
	}
	switch e.Type {
	case pb.FileEntry_FILE:
		return 0o644
	case pb.FileEntry_SYMLINK:
		return os.ModeSymlink | 0o777
	case pb.FileEntry_FIFO:
		return os.ModeNamedPipe | 0o644
	case pb.FileEntry_SOCKET:
		return os.ModeSocket | 0o755
	default:
		return os.ModeIrregular
	}
}

func (c *Client) List(ctx context.Context, volID, dir string, recursive bool) ([]Entry, error) {
	return withRetry(ctx, func(ctx context.Context) ([]Entry, error) {
		p := dir
		if p == "" {
			p = "/"
		}
		s, err := c.Stub.VolumeListFiles2(ctx, pb.VolumeListFiles2Request_builder{VolumeId: volID, Path: p, Recursive: recursive}.Build())
		if err != nil {
			return nil, err
		}
		var out []Entry
		for {
			m, err := s.Recv()
			if err == io.EOF {
				return out, nil
			}
			if err != nil {
				return nil, err
			}
			for _, e := range m.GetEntries() {
				out = append(out, Entry{
					Path:  strings.Trim(e.GetPath(), "/"),
					IsDir: e.GetType() == pb.FileEntry_DIRECTORY,
					Type:  e.GetType(),
					Size:  int64(e.GetSize()),
					Mtime: int64(e.GetMtime()),
				})
			}
		}
	})
}

// Walk lists everything under dir and calls fn with each batch of entries as
// Modal streams them, so a caller can show progress over a large volume.
// Unlike List it doesn't retry: a listing cut short can't be resumed, and a
// retry would count what came before twice.
func (c *Client) Walk(ctx context.Context, volID, dir string, fn func([]Entry)) error {
	p := dir
	if p == "" {
		p = "/"
	}
	s, err := c.Stub.VolumeListFiles2(ctx, pb.VolumeListFiles2Request_builder{VolumeId: volID, Path: p, Recursive: true}.Build())
	if err != nil {
		return err
	}
	for {
		m, err := s.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		batch := make([]Entry, 0, len(m.GetEntries()))
		for _, e := range m.GetEntries() {
			batch = append(batch, Entry{
				Path:  strings.Trim(e.GetPath(), "/"),
				IsDir: e.GetType() == pb.FileEntry_DIRECTORY,
				Type:  e.GetType(),
				Size:  int64(e.GetSize()),
				Mtime: int64(e.GetMtime()),
			})
		}
		fn(batch)
	}
}

// BlockURLs returns signed download URLs for blocks [first, first+n) of path,
// plus the current file size.
func (c *Client) BlockURLs(ctx context.Context, volID, path string, first, n int64) ([]string, int64, error) {
	r, err := withRetry(ctx, func(ctx context.Context) (*pb.VolumeGetFile2Response, error) {
		return c.Stub.VolumeGetFile2(ctx, pb.VolumeGetFile2Request_builder{
			VolumeId: volID, Path: path, Start: uint64(first * BlockSize), Len: uint64(n * BlockSize), ClientPadsBlocks: true,
		}.Build())
	})
	if err != nil {
		return nil, 0, err
	}
	return r.GetGetUrls(), int64(r.GetSize()), nil
}

// Download fetches a signed block URL and pads it to expectLen (the server
// may omit trailing zero bytes when ClientPadsBlocks is set).
func (c *Client) Download(ctx context.Context, url string, expectLen int) ([]byte, error) {
	return transfer(ctx, func(ctx context.Context, w watch) ([]byte, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, status.Error(codes.Unavailable, err.Error())
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			code := codes.Unavailable
			if resp.StatusCode < 500 && resp.StatusCode != 429 {
				code = codes.FailedPrecondition
			}
			return nil, status.Errorf(code, "block GET %d: %s", resp.StatusCode, b)
		}
		buf := make([]byte, expectLen)
		n, err := io.ReadFull(&progressReader{r: resp.Body, moved: w.moved}, buf)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			return nil, status.Error(codes.Unavailable, err.Error())
		}
		// Only a complete body may be short (the rest is zero padding); one
		// cut off before its Content-Length is a failed transfer.
		if err != nil && resp.ContentLength >= 0 && int64(n) < resp.ContentLength {
			return nil, status.Errorf(codes.Unavailable, "block GET: body cut short (%d of %d bytes)", n, resp.ContentLength)
		}
		c.BytesDown.Add(int64(n))
		return buf, nil
	})
}

type PutFile struct {
	Path   string
	Size   int64
	Hashes [][]byte
	Mode   *uint32
	// Block returns the content of block i (only called for blocks the server lacks).
	Block func(i int) ([]byte, error)
}

// Put commits whole-file manifests. Only blocks unknown to Modal's
// content-addressed store are uploaded. Returns number of uploaded blocks.
func (c *Client) Put(ctx context.Context, volID string, files []PutFile) (int, error) {
	return c.PutChecked(ctx, volID, files, nil)
}

// PutChecked is Put with a check that runs after the missing blocks are
// uploaded and right before the request that commits the files; if it
// returns an error, nothing is committed. Uploading can take minutes, so
// this is where to look once more for a concurrent change. If Modal has
// every block already, the first request commits and the check doesn't
// run: the caller's own look must be recent.
func (c *Client) PutChecked(ctx context.Context, volID string, files []PutFile, beforeCommit func() error) (int, error) {
	putResp := make([]map[int][]byte, len(files))
	for i := range putResp {
		putResp[i] = map[int][]byte{}
	}
	uploaded := 0
	useReceipts := true // until Modal refuses one
	for round := 0; round < 6; round++ {
		var pfs []*pb.VolumePutFiles2Request_File
		usedReceipt := false
		for fi, f := range files {
			var blocks []*pb.VolumePutFiles2Request_Block
			for bi, h := range f.Hashes {
				b := pb.VolumePutFiles2Request_Block_builder{ContentsSha256: h}
				if r, ok := putResp[fi][bi]; ok {
					b.PutResponse = r
				} else if r := c.receipts.get(volID, h); r != nil && useReceipts {
					b.PutResponse = r // uploaded ahead (see Ahead)
					usedReceipt = true
				}
				blocks = append(blocks, b.Build())
			}
			fb := pb.VolumePutFiles2Request_File_builder{Path: f.Path, Size: uint64(f.Size), Blocks: blocks}
			if f.Mode != nil {
				fb.Mode = f.Mode
			}
			pfs = append(pfs, fb.Build())
		}
		r, err := withRetry(ctx, func(ctx context.Context) (*pb.VolumePutFiles2Response, error) {
			return c.Stub.VolumePutFiles2(ctx, pb.VolumePutFiles2Request_builder{VolumeId: volID, Files: pfs}.Build())
		})
		if err != nil && usedReceipt && !retryable(err) {
			// A receipt from an upload ahead is refused (expired, or Modal
			// changed how they work): commit without them, uploading again.
			useReceipts = false
			continue
		}
		if err != nil {
			return uploaded, err
		}
		missing := r.GetMissingBlocks()
		if len(missing) == 0 {
			return uploaded, nil
		}
		n, err := c.uploadMissing(ctx, volID, files, missing, putResp)
		uploaded += n
		if err != nil {
			return uploaded, err
		}
		if beforeCommit != nil {
			if err := beforeCommit(); err != nil {
				return uploaded, err
			}
		}
	}
	return uploaded, errors.New("VolumePutFiles2: blocks still missing after retries")
}

// uploadMissing uploads the blocks the server asked for and records the
// responses in putResp. It returns only once every worker has stopped, so no
// Block callback runs after Put returns.
func (c *Client) uploadMissing(ctx context.Context, volID string, files []PutFile, missing []*pb.VolumePutFiles2Response_MissingBlock, putResp []map[int][]byte) (int, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan *pb.VolumePutFiles2Response_MissingBlock, len(missing))
	for _, m := range missing {
		jobs <- m
	}
	close(jobs)
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		uploaded int
		first    error
	)
	for w := 0; w < min(maxUploads, len(missing)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for m := range jobs {
				if ctx.Err() != nil {
					return
				}
				fi, bi := int(m.GetFileIndex()), int(m.GetBlockIndex())
				data, err := files[fi].Block(bi)
				var body []byte
				if err == nil {
					body, err = c.upload(ctx, m.GetPutUrl(), data)
				}
				mu.Lock()
				if err != nil {
					if first == nil {
						first = err
						cancel()
					}
				} else {
					putResp[fi][bi] = body
					c.receipts.put(volID, files[fi].Hashes[bi], body)
					uploaded++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if first == nil {
		first = ctx.Err()
	}
	return uploaded, first
}

// upload sends one block, once a slot is free (see upSlots).
func (c *Client) upload(ctx context.Context, url string, data []byte) ([]byte, error) {
	if err := c.up.acquire(ctx); err != nil {
		return nil, err
	}
	defer c.up.release()
	return transfer(ctx, func(ctx context.Context, w watch) ([]byte, error) {
		newBody := func() io.Reader {
			return &progressReader{r: bytes.NewReader(data), moved: func(n int) { w.moved(n); c.up.moved(n) }, done: w.sent}
		}
		req, _ := http.NewRequestWithContext(ctx, "PUT", url, newBody())
		req.ContentLength = int64(len(data))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(newBody()), nil }
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return nil, status.Error(codes.Unavailable, err.Error())
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode/100 != 2 {
			return nil, status.Errorf(codes.Unavailable, "block PUT %d: %s", resp.StatusCode, truncate(string(body), 200))
		}
		c.BytesUp.Add(int64(len(data)))
		return body, nil
	})
}

func (c *Client) Copy(ctx context.Context, volID string, src []string, dst string, recursive bool) error {
	_, err := withRetry(ctx, func(ctx context.Context) (struct{}, error) {
		_, err := c.Stub.VolumeCopyFiles2(ctx, pb.VolumeCopyFiles2Request_builder{VolumeId: volID, SrcPaths: src, DstPath: dst, Recursive: recursive}.Build())
		return struct{}{}, err
	})
	return err
}

func (c *Client) Remove(ctx context.Context, volID, path string, recursive bool) error {
	_, err := withRetry(ctx, func(ctx context.Context) (struct{}, error) {
		_, err := c.Stub.VolumeRemoveFile2(ctx, pb.VolumeRemoveFile2Request_builder{VolumeId: volID, Path: path, Recursive: recursive}.Build())
		return struct{}{}, err
	})
	return err
}

// HashBlocks splits data into 8 MiB blocks and returns their SHA-256 digests.
func HashBlocks(data []byte) [][]byte {
	var out [][]byte
	for i := 0; i < len(data); i += BlockSize {
		h := sha256.Sum256(data[i:min(i+BlockSize, len(data))])
		out = append(out, h[:])
	}
	return out
}
